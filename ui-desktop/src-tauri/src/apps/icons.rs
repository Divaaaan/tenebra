//! Icons for the application picker, and the encoding they travel in.
//!
//! Icons are the expensive half of a scan — every one is a file read, a decode
//! and a re-encode — so they are collected *after* the list exists, out of
//! whatever is left of the scan budget. Whatever the budget does not cover
//! stays `None`; the picker draws its own placeholder. We never invent one,
//! because a stand-in icon that looks like a real one is worse than an obvious
//! blank when the field it decorates decides which traffic leaves the tunnel.
//!
//! Everything is emitted as a `data:` URI so the webview needs no filesystem
//! access to render it. The encoder here is deliberately tiny: PNG with
//! uncompressed (stored) deflate blocks. It costs bytes compared to a real
//! deflate, which is why the payload is capped at both a count and a byte
//! budget, and why icons are limited to [`MAX_EDGE`] pixels a side.

use std::collections::HashMap;

use super::{AppEntry, AppScan, Budget};

/// The longest edge an icon may have. Anything larger is box-filtered down; a
/// full-size macOS or Windows icon would otherwise dominate the response.
pub(crate) const MAX_EDGE: u32 = 48;

const PNG_SIGNATURE: [u8; 8] = [0x89, b'P', b'N', b'G', 0x0d, 0x0a, 0x1a, 0x0a];

/// Attach icons to as many entries as the remaining budget allows.
///
/// Entries come in already sorted running-first, so when the budget runs out
/// the rows that lost their icon are the ones furthest down the list.
pub(crate) fn fill(scan: &mut AppScan, hints: &HashMap<String, String>, budget: &Budget) {
    let mut produced = 0usize;
    let mut bytes = 0usize;
    let mut stopped = None;

    for entry in scan.apps.iter_mut() {
        if budget.expired() {
            stopped = Some("icons: time budget exhausted");
            break;
        }
        if produced >= super::MAX_ICONS || bytes >= super::MAX_ICON_BYTES {
            stopped = Some("icons: size budget reached");
            break;
        }
        let hint = hints.get(&entry.exe).map(String::as_str);
        if let Some(uri) = load(entry, hint) {
            bytes += uri.len();
            produced += 1;
            entry.icon = Some(uri);
        }
    }

    if let Some(reason) = stopped {
        if !scan.warnings.iter().any(|w| w == reason) {
            scan.warnings.push(reason.to_string());
        }
    }
}

/// Read one entry's icon, in whatever form the platform keeps it.
///
/// `hint` is the platform's own pointer at the artwork when the executable path
/// is not it — on Linux that is the `Icon=` key of the desktop entry, which is
/// a theme name rather than anything derivable from the binary.
fn load(entry: &AppEntry, hint: Option<&str>) -> Option<String> {
    #[cfg(windows)]
    {
        let _ = hint;
        from_executable(entry.path.as_deref()?)
    }
    #[cfg(target_os = "macos")]
    {
        // The bundle walk already located the `.icns`; falling back to the
        // binary path costs a second Info.plist read for the same answer.
        match hint {
            Some(icns) => super::macos::icon_from_icns_file(icns),
            None => super::macos::icon_data_uri(entry.path.as_deref()?),
        }
    }
    #[cfg(target_os = "linux")]
    {
        super::linux::icon_data_uri(hint?)
    }
    #[cfg(not(any(windows, target_os = "macos", target_os = "linux")))]
    {
        let _ = (entry, hint);
        None
    }
}

/// Wrap encoded PNG bytes as the `data:` URI the webview renders.
pub(crate) fn data_uri(png: &[u8]) -> String {
    let mut out = String::with_capacity(22 + png.len().div_ceil(3) * 4);
    out.push_str("data:image/png;base64,");
    base64_into(png, &mut out);
    out
}

/// A PNG's declared dimensions, read from IHDR without decoding pixels.
///
/// Used to accept a file the platform already stores at a usable size — a
/// theme's 48x48 PNG or the small entry of an `.icns` — and pass it through
/// untouched, which is both faster and smaller than re-encoding it.
pub(crate) fn png_dimensions(bytes: &[u8]) -> Option<(u32, u32)> {
    if bytes.len() < 33 || bytes[..8] != PNG_SIGNATURE || &bytes[12..16] != b"IHDR" {
        return None;
    }
    let w = u32::from_be_bytes(bytes[16..20].try_into().ok()?);
    let h = u32::from_be_bytes(bytes[20..24].try_into().ok()?);
    if w == 0 || h == 0 {
        return None;
    }
    Some((w, h))
}

/// Encode straight (non-premultiplied) RGBA as a PNG.
///
/// Returns `None` when the buffer does not match the declared dimensions,
/// rather than encoding whatever it happens to hold.
pub(crate) fn encode_png_rgba(width: u32, height: u32, rgba: &[u8]) -> Option<Vec<u8>> {
    if width == 0 || height == 0 {
        return None;
    }
    let row = (width as usize).checked_mul(4)?;
    let expected = row.checked_mul(height as usize)?;
    if rgba.len() != expected {
        return None;
    }

    // One filter byte per scanline. Filter 0 (None) throughout: the stored
    // deflate below does no matching, so a predictor would only add work.
    let mut raw = Vec::with_capacity(height as usize * (1 + row));
    for y in 0..height as usize {
        raw.push(0);
        raw.extend_from_slice(&rgba[y * row..y * row + row]);
    }

    let mut png = Vec::with_capacity(raw.len() + 128);
    png.extend_from_slice(&PNG_SIGNATURE);

    let mut ihdr = Vec::with_capacity(13);
    ihdr.extend_from_slice(&width.to_be_bytes());
    ihdr.extend_from_slice(&height.to_be_bytes());
    // bit depth 8, colour type 6 (truecolour + alpha), deflate, adaptive
    // filtering, no interlace.
    ihdr.extend_from_slice(&[8, 6, 0, 0, 0]);
    write_chunk(&mut png, b"IHDR", &ihdr);
    write_chunk(&mut png, b"IDAT", &zlib_stored(&raw));
    write_chunk(&mut png, b"IEND", &[]);
    Some(png)
}

/// Box-filter `rgba` down so neither edge exceeds `max`. Returns the source
/// untouched when it already fits.
///
/// Averaging happens on premultiplied alpha, otherwise the fully transparent
/// pixels around an icon's edge drag their (usually black) colour into the
/// visible rim and the result gets a dark halo.
pub(crate) fn fit_rgba(rgba: &[u8], w: u32, h: u32, max: u32) -> Option<(Vec<u8>, u32, u32)> {
    if w == 0 || h == 0 || rgba.len() != (w as usize) * (h as usize) * 4 {
        return None;
    }
    if w <= max && h <= max {
        return Some((rgba.to_vec(), w, h));
    }

    let scale = (max as f32 / w.max(h) as f32).min(1.0);
    let nw = ((w as f32 * scale).round() as u32).clamp(1, max);
    let nh = ((h as f32 * scale).round() as u32).clamp(1, max);
    let mut out = vec![0u8; (nw as usize) * (nh as usize) * 4];

    for y in 0..nh {
        let y0 = (y as u64 * h as u64 / nh as u64) as u32;
        let y1 = (((y + 1) as u64 * h as u64).div_ceil(nh as u64) as u32).max(y0 + 1);
        for x in 0..nw {
            let x0 = (x as u64 * w as u64 / nw as u64) as u32;
            let x1 = (((x + 1) as u64 * w as u64).div_ceil(nw as u64) as u32).max(x0 + 1);

            let (mut r, mut g, mut b, mut a, mut n) = (0u64, 0u64, 0u64, 0u64, 0u64);
            for sy in y0..y1.min(h) {
                for sx in x0..x1.min(w) {
                    let i = ((sy as usize) * (w as usize) + sx as usize) * 4;
                    let alpha = rgba[i + 3] as u64;
                    r += rgba[i] as u64 * alpha;
                    g += rgba[i + 1] as u64 * alpha;
                    b += rgba[i + 2] as u64 * alpha;
                    a += alpha;
                    n += 1;
                }
            }
            let o = ((y as usize) * (nw as usize) + x as usize) * 4;
            // Both divisors can legitimately be zero: `n` when the source block
            // fell outside the image, `a` when every pixel in it is fully
            // transparent. Such a pixel keeps the zero the buffer was
            // initialised with — there is no colour under a zero alpha to
            // un-premultiply, and inventing one would tint the icon's rim.
            out[o + 3] = a.checked_div(n).unwrap_or(0) as u8;
            for (channel, sum) in [r, g, b].into_iter().enumerate() {
                if let Some(value) = sum.checked_div(a) {
                    out[o + channel] = value as u8;
                }
            }
        }
    }
    Some((out, nw, nh))
}

fn write_chunk(out: &mut Vec<u8>, kind: &[u8; 4], data: &[u8]) {
    out.extend_from_slice(&(data.len() as u32).to_be_bytes());
    let start = out.len();
    out.extend_from_slice(kind);
    out.extend_from_slice(data);
    let crc = crc32(&out[start..]);
    out.extend_from_slice(&crc.to_be_bytes());
}

/// A zlib stream of stored (uncompressed) deflate blocks.
///
/// A real deflate would be several times smaller, but it is also several
/// hundred lines of matcher and Huffman tables in a module whose job is reading
/// the system, not compressing. The size that buys back is bounded instead, by
/// the icon count and byte caps in the parent module.
fn zlib_stored(data: &[u8]) -> Vec<u8> {
    // 0x78 0x01: deflate, 32K window, no preset dictionary, fastest level.
    // (0x7801 is divisible by 31, which is the header's own check.)
    let mut out = Vec::with_capacity(data.len() + data.len() / 65535 * 5 + 16);
    out.extend_from_slice(&[0x78, 0x01]);

    if data.is_empty() {
        out.extend_from_slice(&[0x01, 0x00, 0x00, 0xff, 0xff]);
    } else {
        let mut chunks = data.chunks(65_535).peekable();
        while let Some(chunk) = chunks.next() {
            out.push(u8::from(chunks.peek().is_none()));
            let len = chunk.len() as u16;
            out.extend_from_slice(&len.to_le_bytes());
            out.extend_from_slice(&(!len).to_le_bytes());
            out.extend_from_slice(chunk);
        }
    }

    out.extend_from_slice(&adler32(data).to_be_bytes());
    out
}

fn adler32(data: &[u8]) -> u32 {
    let (mut a, mut b) = (1u32, 0u32);
    for &x in data {
        a = (a + x as u32) % 65_521;
        b = (b + a) % 65_521;
    }
    (b << 16) | a
}

/// CRC-32/ISO-HDLC, computed bitwise. Chunks here are a few kilobytes at most,
/// so the table a faster variant would need is not worth carrying.
fn crc32(data: &[u8]) -> u32 {
    let mut crc = 0xFFFF_FFFFu32;
    for &byte in data {
        crc ^= byte as u32;
        for _ in 0..8 {
            crc = if crc & 1 != 0 {
                (crc >> 1) ^ 0xEDB8_8320
            } else {
                crc >> 1
            };
        }
    }
    !crc
}

fn base64_into(data: &[u8], out: &mut String) {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    for chunk in data.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = *chunk.get(1).unwrap_or(&0) as u32;
        let b2 = *chunk.get(2).unwrap_or(&0) as u32;
        let n = (b0 << 16) | (b1 << 8) | b2;
        out.push(ALPHABET[(n >> 18) as usize & 63] as char);
        out.push(ALPHABET[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 {
            ALPHABET[(n >> 6) as usize & 63] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            ALPHABET[n as usize & 63] as char
        } else {
            '='
        });
    }
}

// ---------------------------------------------------------------------------
// Windows: the icon lives inside the executable.
// ---------------------------------------------------------------------------

/// Extract an executable's first icon and encode it.
///
/// Returns `None` for the ordinary cases — a binary with no icon resource, or
/// one we may not read, such as anything under `WindowsApps`, whose ACLs deny
/// the interactive user.
#[cfg(windows)]
fn from_executable(path: &str) -> Option<String> {
    use std::ptr;
    use windows_sys::Win32::UI::Shell::ExtractIconExW;
    use windows_sys::Win32::UI::WindowsAndMessaging::{DestroyIcon, HICON};

    let wide = super::windows::wide(path);
    let mut icon: HICON = ptr::null_mut();
    // SAFETY: `wide` is a NUL-terminated wide string alive across the call, and
    // we ask for exactly one large icon into one slot. A non-zero return means
    // the slot holds an icon we own and must destroy.
    let extracted = unsafe { ExtractIconExW(wide.as_ptr(), 0, &mut icon, ptr::null_mut(), 1) };
    if extracted == 0 || icon.is_null() {
        return None;
    }

    // SAFETY: `icon` is a live HICON we own until DestroyIcon below.
    let rgba = unsafe { hicon_to_rgba(icon) };
    // SAFETY: same handle, destroyed exactly once, not used afterwards.
    unsafe { DestroyIcon(icon) };

    let (rgba, w, h) = rgba?;
    let (rgba, w, h) = fit_rgba(&rgba, w, h, MAX_EDGE)?;
    encode_png_rgba(w, h, &rgba).map(|png| data_uri(&png))
}

/// Copy an icon's pixels out of GDI as straight RGBA.
///
/// # Safety
/// `icon` must be a live `HICON` owned by the caller for the duration.
#[cfg(windows)]
unsafe fn hicon_to_rgba(
    icon: windows_sys::Win32::UI::WindowsAndMessaging::HICON,
) -> Option<(Vec<u8>, u32, u32)> {
    use std::mem::{size_of, zeroed};
    use windows_sys::Win32::Graphics::Gdi::{DeleteObject, GetObjectW, BITMAP};
    use windows_sys::Win32::UI::WindowsAndMessaging::{GetIconInfo, ICONINFO};

    let mut info: ICONINFO = zeroed();
    if GetIconInfo(icon, &mut info) == 0 {
        return None;
    }
    // GetIconInfo hands over two bitmaps we now own; every exit below frees them.
    let (color, mask) = (info.hbmColor, info.hbmMask);

    let result = (|| {
        if color.is_null() {
            // A 1-bit icon: the mask holds the AND and XOR planes stacked. Modern
            // executables do not ship these, and reconstructing one would mean a
            // palette walk for a monochrome result. Not worth it — no icon.
            return None;
        }
        let mut bm: BITMAP = zeroed();
        if GetObjectW(
            color as _,
            size_of::<BITMAP>() as i32,
            &mut bm as *mut _ as *mut core::ffi::c_void,
        ) == 0
        {
            return None;
        }
        let (w, h) = (bm.bmWidth.max(0) as u32, bm.bmHeight.max(0) as u32);
        if w == 0 || h == 0 || w > 1024 || h > 1024 {
            return None;
        }

        let mut pixels = read_bitmap_bgra(color, w, h)?;
        // 32bpp icon bitmaps normally carry their own alpha. Some older ones
        // leave it zeroed, which would render the whole icon invisible; those
        // get their alpha from the AND mask instead (mask set = transparent).
        if pixels.iter().skip(3).step_by(4).all(|&a| a == 0) {
            let mask_px = read_bitmap_bgra(mask, w, h)?;
            for (i, chunk) in pixels.chunks_exact_mut(4).enumerate() {
                let m = mask_px[i * 4];
                chunk[3] = if m == 0 { 255 } else { 0 };
            }
        }

        // BGRA from GDI, RGBA for PNG.
        for chunk in pixels.chunks_exact_mut(4) {
            chunk.swap(0, 2);
        }
        Some((pixels, w, h))
    })();

    if !color.is_null() {
        DeleteObject(color as _);
    }
    if !mask.is_null() {
        DeleteObject(mask as _);
    }
    result
}

/// Read `bitmap` as top-down 32bpp BGRA.
///
/// # Safety
/// `bitmap` must be a live HBITMAP of at least `w` x `h`.
#[cfg(windows)]
unsafe fn read_bitmap_bgra(
    bitmap: windows_sys::Win32::Graphics::Gdi::HBITMAP,
    w: u32,
    h: u32,
) -> Option<Vec<u8>> {
    use std::mem::{size_of, zeroed};
    use std::ptr;
    use windows_sys::Win32::Graphics::Gdi::{
        CreateCompatibleDC, DeleteDC, GetDIBits, BITMAPINFO, BITMAPINFOHEADER, BI_RGB,
        DIB_RGB_COLORS,
    };

    if bitmap.is_null() {
        return None;
    }
    let dc = CreateCompatibleDC(ptr::null_mut());
    if dc.is_null() {
        return None;
    }

    let mut bmi: BITMAPINFO = zeroed();
    bmi.bmiHeader = BITMAPINFOHEADER {
        biSize: size_of::<BITMAPINFOHEADER>() as u32,
        biWidth: w as i32,
        // Negative height asks for a top-down buffer, matching PNG's row order.
        biHeight: -(h as i32),
        biPlanes: 1,
        biBitCount: 32,
        biCompression: BI_RGB,
        ..Default::default()
    };

    let mut buf = vec![0u8; (w as usize) * (h as usize) * 4];
    let lines = GetDIBits(
        dc,
        bitmap,
        0,
        h,
        buf.as_mut_ptr() as *mut core::ffi::c_void,
        &mut bmi,
        DIB_RGB_COLORS,
    );
    DeleteDC(dc);

    if lines == 0 {
        return None;
    }
    Some(buf)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Walk a zlib stream of stored blocks back to the bytes that went in. Lets
    /// the encoder tests assert real round-tripping instead of byte patterns.
    fn inflate_stored(stream: &[u8]) -> Option<Vec<u8>> {
        if stream.len() < 6 || stream[0] != 0x78 {
            return None;
        }
        let mut out = Vec::new();
        let mut i = 2usize;
        loop {
            let header = *stream.get(i)?;
            // Only stored blocks: BTYPE must be 00.
            if header & 0b110 != 0 {
                return None;
            }
            let final_block = header & 1 == 1;
            let len = u16::from_le_bytes(stream.get(i + 1..i + 3)?.try_into().ok()?) as usize;
            let nlen = u16::from_le_bytes(stream.get(i + 3..i + 5)?.try_into().ok()?);
            if nlen != !(len as u16) {
                return None;
            }
            out.extend_from_slice(stream.get(i + 5..i + 5 + len)?);
            i += 5 + len;
            if final_block {
                break;
            }
        }
        // The trailing adler32 must match what we reassembled.
        let adler = u32::from_be_bytes(stream.get(i..i + 4)?.try_into().ok()?);
        if adler != adler32(&out) {
            return None;
        }
        Some(out)
    }

    fn solid(w: u32, h: u32, px: [u8; 4]) -> Vec<u8> {
        px.iter()
            .copied()
            .cycle()
            .take((w as usize) * (h as usize) * 4)
            .collect()
    }

    #[test]
    fn base64_matches_known_vectors() {
        let mut out = String::new();
        base64_into(b"", &mut out);
        assert_eq!(out, "");
        for (input, want) in [
            (&b"f"[..], "Zg=="),
            (&b"fo"[..], "Zm8="),
            (&b"foo"[..], "Zm9v"),
            (&b"foob"[..], "Zm9vYg=="),
            (&b"foobar"[..], "Zm9vYmFy"),
        ] {
            let mut got = String::new();
            base64_into(input, &mut got);
            assert_eq!(got, want, "base64({input:?})");
        }
    }

    #[test]
    fn crc32_and_adler32_match_reference_values() {
        // Both check values are the ones the PNG and zlib specs quote for
        // "123456789" / "abc".
        assert_eq!(crc32(b"123456789"), 0xCBF4_3926);
        assert_eq!(adler32(b"abc"), 0x024D_0127);
    }

    #[test]
    fn encoded_png_has_a_valid_header_and_round_trips() {
        let rgba = vec![
            255, 0, 0, 255, // red
            0, 255, 0, 128, // half-transparent green
            0, 0, 255, 255, // blue
            9, 9, 9, 0, // transparent
        ];
        let png = encode_png_rgba(2, 2, &rgba).expect("encodes");

        assert_eq!(&png[..8], &PNG_SIGNATURE);
        assert_eq!(png_dimensions(&png), Some((2, 2)));
        // colour type 6, bit depth 8, no interlace
        assert_eq!(&png[24..29], &[8, 6, 0, 0, 0]);
        assert!(png.ends_with(&[b'I', b'E', b'N', b'D', 0xAE, 0x42, 0x60, 0x82]));

        // Pull IDAT back out and inflate it: two scanlines, each prefixed with
        // filter byte 0.
        let idat_len = u32::from_be_bytes(png[33..37].try_into().unwrap()) as usize;
        assert_eq!(&png[37..41], b"IDAT");
        let raw = inflate_stored(&png[41..41 + idat_len]).expect("stored deflate round-trips");
        assert_eq!(raw, [&[0][..], &rgba[..8], &[0][..], &rgba[8..]].concat());
    }

    #[test]
    fn encode_rejects_a_buffer_that_does_not_match_its_dimensions() {
        assert!(encode_png_rgba(2, 2, &[0; 12]).is_none());
        assert!(encode_png_rgba(0, 4, &[]).is_none());
    }

    #[test]
    fn png_dimensions_rejects_non_png_bytes() {
        assert!(png_dimensions(b"not a png at all, not even close").is_none());
        assert!(png_dimensions(&[]).is_none());
    }

    #[test]
    fn data_uri_carries_the_png_mime_type() {
        let png = encode_png_rgba(1, 1, &[1, 2, 3, 4]).unwrap();
        let uri = data_uri(&png);
        assert!(uri.starts_with("data:image/png;base64,iVBORw0KGgo"));
    }

    #[test]
    fn fit_leaves_an_already_small_image_alone() {
        let src = solid(16, 16, [10, 20, 30, 255]);
        let (out, w, h) = fit_rgba(&src, 16, 16, MAX_EDGE).unwrap();
        assert_eq!((w, h), (16, 16));
        assert_eq!(out, src);
    }

    #[test]
    fn fit_scales_a_large_icon_within_the_cap_and_keeps_its_colour() {
        let src = solid(256, 256, [10, 20, 30, 255]);
        let (out, w, h) = fit_rgba(&src, 256, 256, MAX_EDGE).unwrap();
        assert_eq!((w, h), (MAX_EDGE, MAX_EDGE));
        assert_eq!(out.len(), (MAX_EDGE as usize).pow(2) * 4);
        // A solid source must average to the same solid colour.
        assert!(out.chunks_exact(4).all(|p| p == [10, 20, 30, 255]));
    }

    #[test]
    fn fit_does_not_bleed_transparent_pixels_into_visible_ones() {
        // Left half opaque white, right half fully transparent black. Straight
        // averaging would darken the result; premultiplied averaging must not.
        let (w, h) = (4u32, 2u32);
        let mut src = Vec::new();
        for _ in 0..h {
            for x in 0..w {
                if x < 2 {
                    src.extend_from_slice(&[255, 255, 255, 255]);
                } else {
                    src.extend_from_slice(&[0, 0, 0, 0]);
                }
            }
        }
        let (out, ow, oh) = fit_rgba(&src, w, h, 2).unwrap();
        assert_eq!((ow, oh), (2, 1));
        assert_eq!(&out[..3], &[255, 255, 255], "left half must stay white");
        assert_eq!(out[3], 255);
    }

    #[test]
    fn fit_rejects_a_mismatched_buffer() {
        assert!(fit_rgba(&[0; 10], 4, 4, 8).is_none());
        assert!(fit_rgba(&[], 0, 0, 8).is_none());
    }

    #[test]
    fn icons_stop_at_an_exhausted_budget_and_report_it() {
        let mut scan = AppScan {
            apps: vec![AppEntry {
                name: "A".into(),
                exe: "a.exe".into(),
                path: Some("a.exe".into()),
                icon: None,
                running: false,
                source: super::super::SOURCE_PROCESS,
            }],
            truncated: false,
            warnings: Vec::new(),
        };
        fill(&mut scan, &HashMap::new(), &Budget::spent());
        assert!(scan.apps[0].icon.is_none());
        assert_eq!(scan.warnings, ["icons: time budget exhausted"]);
    }

    #[test]
    fn a_missing_icon_source_leaves_none_rather_than_a_placeholder() {
        let entry = AppEntry {
            name: "Gone".into(),
            exe: "gone.exe".into(),
            path: Some(r"C:\definitely\not\here\gone.exe".into()),
            icon: None,
            running: false,
            source: super::super::SOURCE_PROCESS,
        };
        assert!(load(&entry, None).is_none());

        // An entry with no path at all is simply skipped.
        let pathless = AppEntry {
            path: None,
            ..entry
        };
        assert!(load(&pathless, None).is_none());
    }
}
