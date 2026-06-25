//! [`ApplyOptions`] — controls how [`super::engine::TemplateEngine::apply`] behaves.

use std::path::PathBuf;

/// Options controlling how [`super::engine::TemplateEngine::apply`] behaves.
#[derive(Debug, Default, Clone)]
pub struct ApplyOptions {
    /// If `true`, do not write any files — return a plan only.
    pub dry_run: bool,

    /// If `true`, overwrite conflicting sections (used for upgrade).
    pub force: bool,

    /// Override the confinement root.  Defaults to `$HOME`.
    /// The target path must be under this directory.
    pub root: Option<PathBuf>,
}
