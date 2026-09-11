//! Cancellable Windows pipe I/O and service authentication. No GUI dependency.
use std::fs::File;
use std::io::{self, Read, Write};
use std::os::windows::io::{AsRawHandle, FromRawHandle, OwnedHandle};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use windows_sys::Win32::Foundation::{ERROR_IO_PENDING, WAIT_OBJECT_0, WAIT_TIMEOUT};
use windows_sys::Win32::Storage::FileSystem::{ReadFile, WriteFile};
use windows_sys::Win32::System::Pipes::GetNamedPipeServerProcessId;
use windows_sys::Win32::System::Services::{
    CloseServiceHandle, OpenSCManagerW, OpenServiceW, QueryServiceConfigW, QueryServiceStatusEx,
    QUERY_SERVICE_CONFIGW, SC_HANDLE, SC_MANAGER_CONNECT, SC_STATUS_PROCESS_INFO,
    SERVICE_QUERY_CONFIG, SERVICE_QUERY_STATUS, SERVICE_RUNNING, SERVICE_STATUS_PROCESS,
    SERVICE_WIN32_OWN_PROCESS,
};
use windows_sys::Win32::System::Threading::{
    CreateEventW, OpenProcess, QueryFullProcessImageNameW, WaitForSingleObject,
    PROCESS_QUERY_LIMITED_INFORMATION,
};
use windows_sys::Win32::System::IO::{CancelIoEx, GetOverlappedResult, OVERLAPPED};

// Mirrored by core/control/pipe_windows.go. Generic write also grants 0x4,
// FILE_CREATE_PIPE_INSTANCE: an interactive client must never request that.
pub const CLIENT_ACCESS: u32 = 0x0012_0083;

pub struct PipeIo {
    file: Arc<File>,
    stop: Arc<AtomicBool>,
    cancelled: Arc<AtomicBool>,
}

impl PipeIo {
    pub fn pair(file: File, stop: Arc<AtomicBool>) -> (Self, Self, Arc<dyn Fn() + Send + Sync>) {
        let file = Arc::new(file);
        let cancelled = Arc::new(AtomicBool::new(false));
        let cancel_flag = Arc::clone(&cancelled);
        (
            Self {
                file: Arc::clone(&file),
                stop: Arc::clone(&stop),
                cancelled: Arc::clone(&cancelled),
            },
            Self {
                file,
                stop,
                cancelled,
            },
            Arc::new(move || {
                cancel_flag.store(true, Ordering::SeqCst);
            }),
        )
    }

    fn cancelled(&self) -> bool {
        self.stop.load(Ordering::SeqCst) || self.cancelled.load(Ordering::SeqCst)
    }

    fn transfer(&self, buf: *mut u8, len: usize, writing: bool) -> io::Result<usize> {
        if self.cancelled() {
            return Err(io::Error::new(
                io::ErrorKind::ConnectionAborted,
                "pipe session cancelled",
            ));
        }
        // Each concurrent operation owns its event and OVERLAPPED. The event,
        // structure and caller buffer stay alive until completion is reaped,
        // INCLUDING after CancelIoEx (cancellation alone is not completion).
        unsafe {
            let event = CreateEventW(std::ptr::null(), 1, 0, std::ptr::null());
            if event.is_null() {
                return Err(io::Error::last_os_error());
            }
            let event = OwnedHandle::from_raw_handle(event);
            let mut op: OVERLAPPED = std::mem::zeroed();
            op.hEvent = event.as_raw_handle();
            let mut count = 0;
            let length = len.min(u32::MAX as usize) as u32;
            let handle = self.file.as_raw_handle();
            let ok = if writing {
                WriteFile(handle, buf, length, &mut count, &mut op)
            } else {
                ReadFile(handle, buf, length, &mut count, &mut op)
            };
            if ok != 0 {
                return Ok(count as usize);
            }
            let error = io::Error::last_os_error();
            if error.raw_os_error() != Some(ERROR_IO_PENDING as i32) {
                return Err(error);
            }
            loop {
                if self.cancelled() {
                    CancelIoEx(handle, &op);
                    // A racing successful completion is fine, but the session
                    // is already cancelled and its reply must not be reused.
                    GetOverlappedResult(handle, &op, &mut count, 1);
                    return Err(io::Error::new(
                        io::ErrorKind::ConnectionAborted,
                        "pipe session cancelled",
                    ));
                }
                match WaitForSingleObject(event.as_raw_handle(), 20) {
                    WAIT_OBJECT_0 => {
                        return if GetOverlappedResult(handle, &op, &mut count, 0) != 0 {
                            Ok(count as usize)
                        } else {
                            Err(io::Error::last_os_error())
                        };
                    }
                    WAIT_TIMEOUT => continue,
                    _ => {
                        let error = io::Error::last_os_error();
                        CancelIoEx(handle, &op);
                        GetOverlappedResult(handle, &op, &mut count, 1);
                        return Err(error);
                    }
                }
            }
        }
    }
}

impl Read for PipeIo {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        if buf.is_empty() || self.cancelled() {
            return Ok(0);
        }
        self.transfer(buf.as_mut_ptr(), buf.len(), false)
    }
}
impl Write for PipeIo {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        if buf.is_empty() {
            return Ok(0);
        }
        self.transfer(buf.as_ptr() as *mut u8, buf.len(), true)
    }
    // WriteFile completes transfer to the pipe buffer. FlushFileBuffers waits
    // for the peer to read and is not cancellable; it must never be used here.
    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

fn identity_matches(server_pid: u32, service_pid: u32, running: bool, local_system: bool) -> bool {
    running && local_system && server_pid != 0 && server_pid == service_pid
}

struct ServiceHandle(SC_HANDLE);
impl Drop for ServiceHandle {
    fn drop(&mut self) {
        unsafe {
            CloseServiceHandle(self.0);
        }
    }
}

unsafe fn wide_string(ptr: *const u16) -> String {
    if ptr.is_null() {
        return String::new();
    }
    let mut length = 0;
    while *ptr.add(length) != 0 {
        length += 1;
    }
    String::from_utf16_lossy(std::slice::from_raw_parts(ptr, length))
}

/// Validate the connected kernel object's server, before sending any payload.
/// SCM configuration is admin protected. Match its LocalSystem own-process
/// service, PID and image; no process-token rights or GUI elevation are needed.
pub fn authenticate_service(file: &File) -> io::Result<()> {
    unsafe {
        let mut server_pid = 0;
        if GetNamedPipeServerProcessId(file.as_raw_handle(), &mut server_pid) == 0 {
            return Err(io::Error::last_os_error());
        }
        let manager = OpenSCManagerW(std::ptr::null(), std::ptr::null(), SC_MANAGER_CONNECT);
        if manager.is_null() {
            return Err(io::Error::last_os_error());
        }
        let name: Vec<u16> = "tenebra\0".encode_utf16().collect();
        let service = OpenServiceW(
            manager,
            name.as_ptr(),
            SERVICE_QUERY_STATUS | SERVICE_QUERY_CONFIG,
        );
        let open_error = io::Error::last_os_error();
        CloseServiceHandle(manager);
        if service.is_null() {
            return Err(open_error);
        }
        let service = ServiceHandle(service);
        let mut status: SERVICE_STATUS_PROCESS = std::mem::zeroed();
        let mut needed = 0;
        let ok = QueryServiceStatusEx(
            service.0,
            SC_STATUS_PROCESS_INFO,
            &mut status as *mut _ as *mut u8,
            std::mem::size_of_val(&status) as u32,
            &mut needed,
        );
        let query_error = io::Error::last_os_error();
        if ok == 0 {
            return Err(query_error);
        }
        // QueryServiceConfig is readable by ordinary authenticated users. A
        // LocalSystem token itself need not grant TOKEN_QUERY to those users.
        let mut config_buffer = [0usize; 1024];
        let config_ptr = config_buffer.as_mut_ptr() as *mut QUERY_SERVICE_CONFIGW;
        let ok = QueryServiceConfigW(
            service.0,
            config_ptr,
            std::mem::size_of_val(&config_buffer) as u32,
            &mut needed,
        );
        let config_error = io::Error::last_os_error();
        if ok == 0 {
            return Err(config_error);
        }
        let config = &*config_ptr;
        let account = wide_string(config.lpServiceStartName);
        let configured_image = wide_string(config.lpBinaryPathName);
        let system = account.eq_ignore_ascii_case("LocalSystem")
            && status.dwServiceType & SERVICE_WIN32_OWN_PROCESS != 0;

        if status.dwCurrentState != SERVICE_RUNNING
            || status.dwProcessId != server_pid
            || server_pid == 0
        {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "pipe server is not the running Tenebra service",
            ));
        }
        let process = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, server_pid);
        if process.is_null() {
            return Err(io::Error::last_os_error());
        }
        let process = OwnedHandle::from_raw_handle(process);
        let mut image_path = vec![0u16; 32768];
        let mut length = image_path.len() as u32;
        if QueryFullProcessImageNameW(
            process.as_raw_handle(),
            0,
            image_path.as_mut_ptr(),
            &mut length,
        ) == 0
        {
            return Err(io::Error::last_os_error());
        }
        let actual_image = String::from_utf16_lossy(&image_path[..length as usize]);
        let registered =
            super::service_policy::registered_image(&configured_image).ok_or_else(|| {
                io::Error::new(
                    io::ErrorKind::PermissionDenied,
                    "Tenebra service has an ambiguous executable path",
                )
            })?;
        if !actual_image.eq_ignore_ascii_case(registered) {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "Tenebra service image differs from its registered executable",
            ));
        }
        // Re-read the connected object's PID while retaining the process
        // handle, preventing PID reuse from validating a replacement process.
        let mut final_status: SERVICE_STATUS_PROCESS = std::mem::zeroed();
        if QueryServiceStatusEx(
            service.0,
            SC_STATUS_PROCESS_INFO,
            &mut final_status as *mut _ as *mut u8,
            std::mem::size_of_val(&final_status) as u32,
            &mut needed,
        ) == 0
        {
            return Err(io::Error::last_os_error());
        }
        let mut final_pid = 0;
        if GetNamedPipeServerProcessId(file.as_raw_handle(), &mut final_pid) == 0
            || final_status.dwProcessId != server_pid
            || !identity_matches(
                final_pid,
                final_status.dwProcessId,
                final_status.dwCurrentState == SERVICE_RUNNING,
                system && final_status.dwServiceType & SERVICE_WIN32_OWN_PROCESS != 0,
            )
        {
            return Err(io::Error::new(
                io::ErrorKind::PermissionDenied,
                "Tenebra pipe server identity could not be verified",
            ));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs::OpenOptions;
    use std::os::windows::fs::OpenOptionsExt;
    use std::sync::mpsc;
    use std::thread;
    use std::time::{Duration, Instant};
    use windows_sys::Win32::Foundation::{ERROR_PIPE_CONNECTED, INVALID_HANDLE_VALUE};
    use windows_sys::Win32::Storage::FileSystem::{
        FILE_FLAG_FIRST_PIPE_INSTANCE, FILE_FLAG_OVERLAPPED, PIPE_ACCESS_DUPLEX,
    };
    use windows_sys::Win32::System::Pipes::{
        ConnectNamedPipe, CreateNamedPipeW, PIPE_TYPE_BYTE, PIPE_WAIT,
    };
    #[test]
    fn only_running_registered_system_process_is_trusted() {
        assert!(identity_matches(123, 123, true, true));
        assert!(!identity_matches(124, 123, true, true));
        assert!(!identity_matches(0, 0, true, true));
        assert!(!identity_matches(123, 123, false, true));
        assert!(!identity_matches(123, 123, true, false));
    }
    #[test]
    fn interactive_access_excludes_instance_creation_and_dacl_mutation() {
        assert_eq!(CLIENT_ACCESS & (0x4 | 0x40000 | 0x80000), 0);
        assert_eq!(CLIENT_ACCESS & 3, 3);
    }

    // Isolated kernel pipe only. No service, real core, routes or privileged
    // operations. Deliberately unread input fills the small server buffer.
    fn blocked_operation_is_cancelled(writing: bool) {
        let name = format!(
            r"\\.\pipe\tenebra-cancel-test-{}-{}",
            std::process::id(),
            writing
        );
        let server_name = name.clone();
        let (ready_tx, ready_rx) = mpsc::channel();
        let (release_tx, release_rx) = mpsc::channel::<()>();
        let server = thread::spawn(move || unsafe {
            let wide: Vec<u16> = server_name.encode_utf16().chain(Some(0)).collect();
            let handle = CreateNamedPipeW(
                wide.as_ptr(),
                PIPE_ACCESS_DUPLEX | FILE_FLAG_FIRST_PIPE_INSTANCE,
                PIPE_TYPE_BYTE | PIPE_WAIT,
                1,
                4096,
                4096,
                0,
                std::ptr::null(),
            );
            assert_ne!(handle, INVALID_HANDLE_VALUE);
            let file = File::from_raw_handle(handle);
            ready_tx.send(()).unwrap();
            if ConnectNamedPipe(file.as_raw_handle(), std::ptr::null_mut()) == 0 {
                assert_eq!(
                    io::Error::last_os_error().raw_os_error(),
                    Some(ERROR_PIPE_CONNECTED as i32)
                );
            }
            let _ = release_rx.recv_timeout(Duration::from_secs(3));
        });
        ready_rx.recv_timeout(Duration::from_secs(2)).unwrap();
        let file = OpenOptions::new()
            .access_mode(CLIENT_ACCESS)
            .custom_flags(FILE_FLAG_OVERLAPPED)
            .open(name)
            .unwrap();
        let (mut reader, mut writer, cancel) = PipeIo::pair(file, Arc::new(AtomicBool::new(false)));
        let (done_tx, done_rx) = mpsc::channel();
        let caller = thread::spawn(move || {
            let result = if writing {
                writer.write_all(&vec![42; 1024 * 1024])
            } else {
                reader.read(&mut [0u8; 1]).map(|_| ())
            };
            let _ = done_tx.send(result);
        });
        assert!(
            done_rx.recv_timeout(Duration::from_millis(40)).is_err(),
            "I/O must actually be blocked before cancellation"
        );
        let started = Instant::now();
        cancel();
        let result = done_rx.recv_timeout(Duration::from_secs(1));
        drop(release_tx);
        server.join().unwrap();
        caller.join().unwrap();
        assert!(result.unwrap().is_err());
        assert!(started.elapsed() < Duration::from_secs(1));
    }

    #[test]
    fn overlapped_backpressure_write_is_cancelled_and_reaped() {
        blocked_operation_is_cancelled(true);
    }
    #[test]
    fn overlapped_idle_read_is_cancelled_and_reaped() {
        blocked_operation_is_cancelled(false);
    }
}
