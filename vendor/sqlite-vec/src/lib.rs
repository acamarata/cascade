#[link(name = "sqlite_vec0")]
extern "C" {
    pub fn sqlite3_vec_init(
        db: *mut libsqlite3_sys::sqlite3,
        error: *mut *const std::ffi::c_char,
        api: *const libsqlite3_sys::sqlite3_api_routines,
    ) -> std::ffi::c_int;
}
