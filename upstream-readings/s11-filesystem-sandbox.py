# Source: browser_use/filesystem/file_system.py (excerpts)
# License: MIT (Copyright 2024 Gregor Zunic)
# Upstream SHA: 933e28c599ddd74c15a48568f159da95547e40dd
# Annotated by learn-browser-use for chapter s11.
#
# Three excerpts: (1) module-scope deny-list + LLM-friendly error builder,
# (2) the filename regex + sanitize_filename pair, (3) _resolve_filename
# (which does the basename-strip "traversal defense by amnesia") plus
# FileSystem.__init__ (which decides where the sandbox actually lives).
#
# Orientation: upstream's strategy is "sanitize then proceed" — take
# os.path.basename, strip exotic characters, append a fallback name if
# nothing's left, then write. Our Go strategy is "detect then refuse" —
# IsSafePath rejects the input rather than silently rewriting it. Two
# different philosophies; the README's "What this is NOT" section
# explains why we chose refusal for a teaching repo.


# ── (1) Module-scope deny-list and LLM-facing error message ──

# Source: browser_use/filesystem/file_system.py#L15-L73

UNSUPPORTED_BINARY_EXTENSIONS = {
    'png', 'jpg', 'jpeg', 'gif', 'bmp', 'svg', 'webp', 'ico',
    'mp3', 'mp4', 'wav', 'avi', 'mov',
    'zip', 'tar', 'gz', 'rar',
    'exe', 'bin', 'dll', 'so',
}
# ↑ Module-scope, not per-instance configurable. Our Go's
#   defaultBinaryDenylist in safety.go is a 1:1 port of this set
#   (plus .dylib for macOS). The fact that this lives at module
#   scope rather than as a constructor parameter is the design
#   decision we copy explicitly: a relaxed AllowedExts mustn't be
#   able to re-enable .png.


def _build_filename_error_message(file_name: str, supported_extensions: list[str]) -> str:
    """Build a specific error message explaining why the filename was rejected."""
    base = os.path.basename(file_name)
    # ↑ Upstream uses os.path.basename as the FIRST line of defense.
    #   "../../etc/passwd" → "passwd". Our Go version refuses the
    #   `..`-bearing input instead — losing the friendly behaviour but
    #   gaining a tractable signal for tests + audit logs.

    if '.' in base:
        _, ext = base.rsplit('.', 1)
        ext_lower = ext.lower()
        if ext_lower in UNSUPPORTED_BINARY_EXTENSIONS:
            return (
                f"Error: Cannot write binary/image file '{base}'. "
                f'The write_file tool only supports text-based files. '
                # ↑ The error message is LLM-facing. It explains
                #   WHY the operation failed and HINTS at an
                #   alternative ("the browser captures screenshots
                #   automatically"). Our Go errors are terser; the
                #   s12 tool layer is supposed to translate them
                #   into LLM-readable text on the way out.
                f'Supported extensions: {", ".join("." + e for e in supported_extensions)}. '
                f'For screenshots, the browser automatically captures them - '
                f'do not try to save screenshots as files.'
            )


# ── (2) Filename regex + sanitize_filename ──

# Source: browser_use/filesystem/file_system.py#L407-L450

def _is_valid_filename(self, file_name: str) -> bool:
    """Check if filename matches the required pattern: name.extension"""
    extensions = '|'.join(self._file_types.keys())
    # ↑ The regex restricts the name part to:
    #     a-z, A-Z, 0-9, _-.()
    #     spaces (yes, spaces are legal here)
    #     一-鿿 (CJK Unified Ideographs — Chinese)
    #   Our Go version doesn't gate names on a regex at all; we let
    #   the OS reject illegal names. The chapter is about path
    #   safety, not name sanitization — they're orthogonal concerns.
    pattern = rf'^[a-zA-Z0-9_\-\.\(\) 一-鿿]+\.({extensions})$'
    file_name_base = os.path.basename(file_name)
    if not re.match(pattern, file_name_base):
        return False
    name_part = file_name_base.rsplit('.', 1)[0]
    return len(name_part.strip()) > 0


@staticmethod
def sanitize_filename(file_name: str) -> str:
    """Sanitize a filename by replacing/removing invalid characters."""
    base = os.path.basename(file_name)
    if '.' not in base:
        return base

    name_part, ext = base.rsplit('.', 1)
    name_part = name_part.replace(' ', '-')
    # ↑ Upstream silently rewrites "my notes.txt" to "my-notes.txt"
    #   because LLMs frequently emit spaces in filenames. Our Go
    #   accepts the original; spaces are perfectly legal POSIX names
    #   and the shell-quoting concerns are not a sandbox issue.
    name_part = re.sub(r'[^a-zA-Z0-9_\-\.\(\)一-鿿]', '', name_part)
    name_part = re.sub(r'-{2,}', '-', name_part)
    name_part = name_part.strip('-.')

    if not name_part:
        name_part = 'file'

    return f'{name_part}.{ext.lower()}'


# ── (3) _resolve_filename + FileSystem.__init__ — where the sandbox lives ──

# Source: browser_use/filesystem/file_system.py#L451-L475

def _resolve_filename(self, file_name: str) -> tuple[str, bool]:
    """Resolve a filename, attempting sanitization if the original is invalid.

    Normalizes to basename first to prevent directory traversal (e.g. ../secret.md).
    """
    base_name = os.path.basename(file_name)
    was_changed = base_name != file_name
    # ↑ This is upstream's path-traversal "defense". Strip directory
    #   components, keep going. Effective against the obvious cases
    #   but loses information — you can't tell from a successful
    #   write whether the LLM tried something funny. Our IsSafePath
    #   surfaces the attempt in an error.

    if self._is_valid_filename(base_name):
        return base_name, was_changed
    sanitized = self.sanitize_filename(base_name)
    if sanitized != base_name and self._is_valid_filename(sanitized):
        return sanitized, True
    return base_name, was_changed


# Source: browser_use/filesystem/file_system.py#L356-L383

def __init__(self, base_dir: str | Path, create_default_files: bool = True):
    self.base_dir = Path(base_dir) if isinstance(base_dir, str) else base_dir
    self.base_dir.mkdir(parents=True, exist_ok=True)

    # ↑ The sandbox actually lives in a subdirectory named
    #   "browseruse_agent_data" inside base_dir. Our Go LocalFileSystem
    #   takes Root directly — no automatic subdir nesting. Slightly
    #   different posture: upstream picks the directory for you
    #   ("we own this folder, you own its parent"); we let the caller
    #   pick exactly where the sandbox is.
    self.data_dir = self.base_dir / DEFAULT_FILE_SYSTEM_PATH
    if self.data_dir.exists():
        shutil.rmtree(self.data_dir)
        # ↑ Upstream wipes the data dir on every construction. That's
        #   a stronger isolation posture than ours — every Agent.run()
        #   starts on a clean slate. We don't do this; persisting
        #   across runs is a teaching feature (you can write a .txt
        #   in one go run and read it in the next). Real deployments
        #   would mirror upstream and wipe.
    self.data_dir.mkdir(exist_ok=True)

    self._file_types: dict[str, type[BaseFile]] = {
        'md': MarkdownFile,
        'txt': TxtFile,
        'json': JsonFile,
        'jsonl': JsonlFile,
        'csv': CsvFile,
        'pdf': PdfFile,
        'docx': DocxFile,
        'html': HtmlFile,
        'xml': XmlFile,
    }
    # ↑ Upstream binds each extension to a typed class that knows
    #   how to serialize (PdfFile via reportlab, DocxFile via
    #   python-docx, CsvFile with RFC 4180 normalization). Our Go
    #   version is "all files are strings". The pdf/docx handlers
    #   are deliberately omitted; the CSV normalization would be a
    #   nice future exercise.
