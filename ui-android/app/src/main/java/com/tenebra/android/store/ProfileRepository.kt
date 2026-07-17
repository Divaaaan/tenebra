package com.tenebra.android.store

import android.content.Context
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.io.File

// The single source of truth for the imported profile and node selection, shared by
// the UI (MainViewModel) and the tunnel (TenebraVpnService). Both run in the same
// process, so one app-scoped instance with in-memory StateFlows backed by disk is
// enough — no cross-process store needed.
//
// The profile is the RAW JSON the core bridge returned from importSubscription,
// stored verbatim (never re-encoded) so it round-trips into generateConfig/orderNodes
// exactly. It is written atomically. Scalars (selection, last-good) live in
// SharedPreferences so the service can read them synchronously at connect time.
class ProfileRepository private constructor(context: Context) {

    private val appContext = context.applicationContext
    private val profileFile = File(appContext.filesDir, PROFILE_FILE)
    private val prefs = appContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    private val _profileJson = MutableStateFlow(readProfileFromDisk())
    val profileJson: StateFlow<String?> = _profileJson.asStateFlow()

    private val _selectedServerId = MutableStateFlow(prefs.getString(KEY_SELECTED, null))
    val selectedServerId: StateFlow<String?> = _selectedServerId.asStateFlow()

    // --- profile ---

    @Synchronized
    fun saveProfile(rawJson: String) {
        writeAtomically(profileFile, rawJson)
        _profileJson.value = rawJson
    }

    fun currentProfileJson(): String? = _profileJson.value

    @Synchronized
    fun clearProfile() {
        profileFile.delete()
        _profileJson.value = null
        setSelectedServerId(null)
    }

    // --- selection ---

    fun setSelectedServerId(id: String?) {
        prefs.edit().apply {
            if (id == null) remove(KEY_SELECTED) else putString(KEY_SELECTED, id)
        }.apply()
        _selectedServerId.value = id
    }

    fun currentSelectedServerId(): String? = _selectedServerId.value

    // --- last-good (leads the fallback walk on the next connect) ---

    fun setLastGoodServerId(id: String?) {
        prefs.edit().apply {
            if (id == null) remove(KEY_LAST_GOOD) else putString(KEY_LAST_GOOD, id)
        }.apply()
    }

    fun currentLastGoodServerId(): String? = prefs.getString(KEY_LAST_GOOD, null)

    // --- disk ---

    private fun readProfileFromDisk(): String? =
        runCatching { if (profileFile.exists()) profileFile.readText() else null }.getOrNull()

    // Write to a temp sibling then rename, so a crash mid-write can never leave a
    // truncated profile on disk.
    private fun writeAtomically(target: File, content: String) {
        val tmp = File(target.parentFile, target.name + ".tmp")
        tmp.writeText(content)
        if (!tmp.renameTo(target)) {
            // renameTo can fail across some filesystems; fall back to a direct copy.
            target.writeText(content)
            tmp.delete()
        }
    }

    companion object {
        private const val PROFILE_FILE = "current-profile.json"
        private const val PREFS = "tenebra"
        private const val KEY_SELECTED = "selected_server_id"
        private const val KEY_LAST_GOOD = "last_good_server_id"

        @Volatile
        private var instance: ProfileRepository? = null

        fun getInstance(context: Context): ProfileRepository =
            instance ?: synchronized(this) {
                instance ?: ProfileRepository(context).also { instance = it }
            }
    }
}
