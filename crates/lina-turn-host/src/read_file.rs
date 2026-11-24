//! Local read_file. File bytes leave this module only through the private
//! redaction exchange. Only its approved replacement enters a tool result.

use std::collections::HashMap;
use std::fs::File;
use std::future::Future;
use std::io::{self, BufRead, BufReader, Read};
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

#[derive(Clone, Deserialize, PartialEq, Serialize)]
struct Identity {
    dev: u64,
    ino: u64,
    size: u64,
    mtime_ns: i64,
    ctime_ns: i64,
}

#[derive(Deserialize)]
struct Grant {
    resolved: PathBuf,
    display_path: String,
    identity: Identity,
    max_chars: usize,
    max_line_length: usize,
    max_lines: u64,
}

#[derive(Default)]
pub(crate) struct ReadState {
    seen: HashMap<(PathBuf, u64, u64), (Identity, u64)>,
}

fn unavailable() -> Value {
    json!({"error":"File policy is unavailable for this read."})
}

fn changed() -> Value {
    json!({"error":"File changed during authorization. Retry the read."})
}

fn decision(ack: Option<Value>) -> Result<Value, Value> {
    let reply = ack.ok_or_else(unavailable)?;
    if reply["ok"] == true {
        return Ok(reply);
    }
    if let Some(error) = reply["error"].as_str() {
        return Err(json!({"error":error}));
    }
    Err(reply
        .get("result")
        .filter(|r| r.is_object())
        .cloned()
        .unwrap_or_else(unavailable))
}

fn positive(value: &Value, default: u64) -> u64 {
    let n = value
        .as_u64()
        .or_else(|| value.as_i64().map(|_| 1))
        .or_else(|| {
            value
                .as_str()?
                .trim()
                .parse::<i128>()
                .ok()
                .map(|n| u64::try_from(n.max(1)).unwrap_or(u64::MAX))
        })
        .or_else(|| {
            value
                .as_f64()
                .and_then(|n| n.trunc().to_string().parse::<i128>().ok())
                .map(|n| u64::try_from(n.max(1)).unwrap_or(u64::MAX))
        });
    n.unwrap_or(default).max(1)
}

impl ReadState {
    pub(crate) async fn read<F, Fut>(
        &mut self,
        args: &Value,
        subdirectory_hints: &mut Option<String>,
        mut policy: F,
    ) -> Value
    where
        F: FnMut(Value) -> Fut,
        Fut: Future<Output = Option<Value>>,
    {
        let Some(raw) = args["path"].as_str() else {
            return json!({"error":"read_file requires a path string."});
        };
        let grant = match decision(policy(json!({"op":"authorize","path":raw})).await) {
            Ok(reply) => serde_json::from_value::<Grant>(reply),
            Err(result) => return result,
        };
        let Ok(grant) = grant else {
            return unavailable();
        };
        if !grant.resolved.is_absolute()
            || grant.max_chars == 0
            || grant.max_lines == 0
            || grant.max_line_length == 0
        {
            return unavailable();
        }
        let offset = positive(&args["offset"], 1);
        let limit = positive(&args["limit"], 2000).min(grant.max_lines);
        let key = (grant.resolved.clone(), offset, limit);
        if let Some((id, hits)) = self.seen.get_mut(&key)
            && *id == grant.identity
        {
            *hits = hits.saturating_add(1);
            return if *hits == 1 {
                json!({"status":"unchanged","message":"File unchanged since last read. The content from the earlier read_file result in this conversation is still current — refer to that instead of re-reading.",
                    "path":grant.display_path,"dedup":true,"content_returned":false})
            } else {
                let count = hits.saturating_add(1);
                json!({"error":format!("BLOCKED: You have called read_file on this exact region {count} times and the file has NOT changed. STOP calling read_file for this path — the content from your earlier read_file result in this conversation is still current. Proceed with your task using the information you already have."),
                    "path":grant.display_path,"already_read":count,"guardrail_refusal":true})
            };
        }
        let redaction = json!({"op":"redact","path":raw,"resolved":grant.resolved,
            "identity":grant.identity});
        let identity = grant.identity.clone();
        let read = tokio::task::spawn_blocking(move || read_page(&grant, offset, limit)).await;
        let Ok(page) = read else { return unavailable() };
        let mut result = match page {
            Ok(result) => result,
            Err(refusal) => return refusal,
        };
        let mut request = redaction;
        request["content"] = result["content"].take();
        match decision(policy(request).await) {
            Ok(mut reply) if reply["content"].is_string() => {
                result["content"] = reply["content"].take();
                *subdirectory_hints = reply["hints"].as_str().map(str::to_owned);
            }
            Ok(_) => return unavailable(),
            Err(result) => return result,
        }
        add_hints(&mut result, limit);
        if self.seen.len() >= 1024 {
            self.seen.clear();
        }
        if result["extracted_document"] != true {
            self.seen.insert(key, (identity, 0));
        }
        result
    }
}

#[cfg(target_os = "linux")]
fn open_granted(grant: &Grant) -> Result<File, Value> {
    use rustix::fs::{Mode, OFlags, open};
    use std::os::{fd::AsRawFd, unix::fs::MetadataExt};

    // O_PATH cannot trigger a driver open or wait on a FIFO. Reopen only
    // through the pinned descriptor, never the caller's pathname.
    let pin = File::from(
        open(
            &grant.resolved,
            OFlags::PATH | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::empty(),
        )
        .map_err(|_| changed())?,
    );
    let st = pin.metadata().map_err(|_| changed())?;
    let ns = |s: i64, n: i64| s.checked_mul(1_000_000_000)?.checked_add(n);
    let id = &grant.identity;
    if !st.is_file()
        || st.dev() != id.dev
        || st.ino() != id.ino
        || st.len() != id.size
        || ns(st.mtime(), st.mtime_nsec()) != Some(id.mtime_ns)
        || ns(st.ctime(), st.ctime_nsec()) != Some(id.ctime_ns)
    {
        return Err(changed());
    }
    let fd_path = format!("/proc/self/fd/{}", pin.as_raw_fd());
    if std::fs::read_link(&fd_path).ok().as_deref() != Some(grant.resolved.as_path()) {
        return Err(changed());
    }
    File::open(fd_path).map_err(|_| json!({"error":"Failed to open authorized file."}))
}

#[cfg(not(target_os = "linux"))]
fn open_granted(_: &Grant) -> Result<File, Value> {
    Err(json!({"error":"Pinned local file reads are unavailable on this platform."}))
}

fn blank(size: u64) -> Value {
    json!({"content":"","total_lines":0,"file_size":size,"truncated":false})
}

fn extension(path: &Path) -> String {
    path.extension()
        .and_then(|s| s.to_str())
        .unwrap_or_default()
        .to_ascii_lowercase()
}

fn extension_refusal(ext: &str, raw: &str) -> Option<Value> {
    let docs = "doc docm docx xls xlsx xlsm xlsb ppt pps pot pptx pptm ppsx ppsm odt ods odp rtf epub pdf db sqlite sqlite3";
    if docs.split_whitespace().any(|s| s == ext) {
        return Some(
            json!({"error":format!("Cannot read '{raw}' (.{ext}): document extraction is unavailable in the Rust host.")}),
        );
    }
    let binary = "png jpg jpeg gif bmp ico webp tiff tif mp4 mov avi mkv webm wmv flv m4v mpeg mpg mp3 wav ogg flac aac m4a wma aiff opus zip tar gz bz2 7z rar xz z tgz iso exe dll so dylib bin o a obj lib app msi deb rpm ttf otf woff woff2 eot pyc pyo class jar war ear node wasm rlib mdb idx psd ai eps sketch fig xd blend 3ds max swf fla lockb dat data";
    let sidecar = ext.split_once('-').is_some_and(|(db, suffix)| {
        matches!(db, "db" | "sqlite" | "sqlite3") && matches!(suffix, "shm" | "wal" | "journal")
    });
    (sidecar || binary.split_whitespace().any(|s| s == ext)).then(||
        json!({"error":format!("Cannot read binary file '{raw}' (.{ext}). Use vision_analyze for images, or terminal to inspect binary files.")}))
}

fn read_page(grant: &Grant, offset: u64, limit: u64) -> Result<Value, Value> {
    let ext = extension(&grant.resolved);
    if let Some(reason) = extension_refusal(&ext, &grant.display_path) {
        return Err(reason);
    }
    let size = grant.identity.size;
    let file = open_granted(grant)?;
    let io_error = |_: io::Error| json!({"error":"Failed to read authorized file."});
    let mut reader = BufReader::new(file.take(size));
    let buf = reader.fill_buf().map_err(io_error)?;
    let sample = &buf[..buf.len().min(1000)];
    if sample.contains(&0)
        || std::str::from_utf8(sample)
            .is_err_and(|e| e.valid_up_to() < sample.len().saturating_sub(3))
    {
        if size <= 10 * 1024 * 1024
            && let Some(little) = utf16_order(sample)
        {
            let mut bytes = Vec::new();
            reader.read_to_end(&mut bytes).map_err(io_error)?;
            return utf16_page(&bytes, little, grant, offset, limit).map_err(io_error);
        }
        let mut result = blank(size);
        result["is_binary"] = json!(true);
        result["is_image"] = json!(false);
        result["error"] = json!(binary_description(sample, size));
        result["not_found"] = json!(false);
        return Err(result);
    }
    if ext == "ipynb" {
        // Never render raw notebook JSON (rich output can include giant image payloads).
        if size > 50 * 1024 * 1024 {
            return Err(json!({"error":"Notebook exceeds the 50 MiB document extraction limit."}));
        }
        let notebook: Value = serde_json::from_reader(reader)
            .map_err(|_| json!({"error":"Cannot extract notebook: invalid notebook JSON."}))?;
        let text = notebook_text(&notebook)?;
        let result = stream_page(text.as_bytes(), grant, offset, limit, Format::Notebook)
            .map_err(io_error)?;
        return Ok(result);
    }
    stream_page(reader, grant, offset, limit, Format::Text).map_err(io_error)
}

fn utf16_order(sample: &[u8]) -> Option<bool> {
    if sample.starts_with(&[0xff, 0xfe]) {
        return Some(true);
    }
    if sample.starts_with(&[0xfe, 0xff]) {
        return Some(false);
    }
    let mut zeros = [0; 2];
    for (i, byte) in sample.iter().take(512).enumerate() {
        zeros[i % 2] += usize::from(*byte == 0);
    }
    match zeros {
        [0, odd] if odd >= 2 => Some(true),
        [even, 0] if even >= 2 => Some(false),
        _ => None,
    }
}

fn utf16_page(
    bytes: &[u8],
    little: bool,
    grant: &Grant,
    offset: u64,
    limit: u64,
) -> io::Result<Value> {
    let (pairs, remainder) = bytes.as_chunks::<2>();
    let units = pairs.iter().map(|p| {
        if little {
            u16::from_le_bytes(*p)
        } else {
            u16::from_be_bytes(*p)
        }
    });
    let mut text: String = char::decode_utf16(units)
        .map(|ch| ch.unwrap_or(char::REPLACEMENT_CHARACTER))
        .collect();
    if !remainder.is_empty() {
        text.push(char::REPLACEMENT_CHARACTER);
    }
    let text = text
        .strip_prefix('\u{feff}')
        .unwrap_or(&text)
        .replace("\r\n", "\n");
    let label = if little { "UTF-16-LE" } else { "UTF-16-BE" };
    stream_page(text.as_bytes(), grant, offset, limit, Format::Utf16(label))
}

fn binary_description(sample: &[u8], size: u64) -> String {
    let kinds: &[(&[u8], &str)] = &[
        (b"\x89PNG\r\n\x1a\n", "PNG image data"),
        (b"\xff\xd8\xff", "JPEG image data"),
        (b"GIF87a", "GIF image data"),
        (b"GIF89a", "GIF image data"),
        (b"%PDF-", "PDF document"),
        (b"PK\x03\x04", "ZIP archive (also docx/xlsx/jar/apk)"),
        (b"\x7fELF", "ELF executable"),
        (b"SQLite format 3\0", "SQLite database"),
        (b"\x1f\x8b", "gzip compressed data"),
    ];
    let kind = kinds
        .iter()
        .find(|(magic, _)| sample.starts_with(magic))
        .map_or("unknown binary", |(_, s)| *s);
    let amount = if size >= 1 << 20 {
        format!("{:.1} MB", size as f64 / 1048576.0)
    } else if size >= 1024 {
        format!("{:.1} KB", size as f64 / 1024.0)
    } else {
        format!("{size} bytes")
    };
    format!("Binary file ({kind}, {amount}) — cannot display as text.")
}

fn notebook_text(notebook: &Value) -> Result<String, Value> {
    let cells = notebook["cells"]
        .as_array()
        .ok_or_else(|| json!({"error":"Unsupported notebook structure."}))?;
    let mut counts = [0; 2];
    let mut text = String::new();
    for cell in cells {
        let label = match cell["cell_type"].as_str() {
            Some("markdown") => {
                counts[0] += 1;
                format!("Markdown cell {}", counts[0])
            }
            Some("code") => {
                counts[1] += 1;
                format!("Code cell {}", counts[1])
            }
            Some("raw") => "Raw cell".into(),
            _ => continue,
        };
        text.push_str(&format!("# ── {label} ──\n"));
        let source = |v: &Value| match v.as_array() {
            Some(parts) => parts.iter().filter_map(Value::as_str).collect::<String>(),
            None => v.as_str().unwrap_or_default().to_owned(),
        };
        text.push_str(source(&cell["source"]).trim_end_matches('\n'));
        text.push_str("\n\n");
        if let Some(outputs) = cell["outputs"].as_array().filter(|items| !items.is_empty()) {
            let mut rendered = Vec::new();
            for output in outputs {
                let value = match output["output_type"].as_str() {
                    Some("stream") => source(&output["text"]),
                    Some("execute_result" | "display_data")
                        if output["data"]["text/plain"].is_string()
                            || output["data"]["text/plain"].is_array() =>
                    {
                        source(&output["data"]["text/plain"])
                    }
                    _ => {
                        return Err(
                            json!({"error":"This notebook output format requires the document extractor."}),
                        );
                    }
                };
                if !value.trim().is_empty() {
                    rendered.push(value);
                }
            }
            if !rendered.is_empty() {
                text.push_str(&format!(
                    "# ── Output (cell {}) ──\n{}\n\n",
                    counts[1],
                    rendered.join("\n").trim_end_matches('\n')
                ));
            }
        }
    }
    if text.is_empty() {
        return Err(json!({"error":"Notebook contains no readable cells."}));
    }
    text.truncate(text.trim_end_matches('\n').len());
    text.push('\n');
    Ok(text)
}

struct Page {
    content: String,
    chars: usize,
    kept: u64,
    shortened: bool,
    full: bool,
}

impl Page {
    fn line(&mut self, bytes: &[u8], number: u64, grant: &Grant) {
        let decoded = String::from_utf8_lossy(bytes);
        let line = if number == 1 {
            decoded.strip_prefix('\u{feff}').unwrap_or(&decoded)
        } else {
            &decoded
        };
        let mut chars = line.chars();
        let mut formatted = format!("{number}|");
        formatted.extend(chars.by_ref().take(grant.max_line_length));
        if chars.next().is_some() {
            self.shortened = true;
            formatted.push_str("... [truncated]");
        }
        if self.full {
            return;
        }
        let separator = usize::from(self.kept != 0);
        let width = formatted.chars().count();
        if width.saturating_add(separator) > grant.max_chars - self.chars {
            self.full = true;
            if self.kept == 0 {
                self.content.extend(formatted.chars().take(grant.max_chars));
                self.kept = 1;
            }
            return;
        }
        if separator != 0 {
            self.content.push('\n');
        }
        self.content.push_str(&formatted);
        self.chars += separator + width;
        self.kept += 1;
    }
}

enum Format {
    Text,
    Utf16(&'static str),
    Notebook,
}

fn stream_page(
    mut input: impl BufRead,
    grant: &Grant,
    offset: u64,
    limit: u64,
    format: Format,
) -> io::Result<Value> {
    let end = offset.saturating_add(limit - 1);
    let cap = grant.max_line_length.saturating_mul(4).saturating_add(1);
    let mut page = Page {
        content: String::new(),
        chars: 0,
        kept: 0,
        shortened: false,
        full: false,
    };
    let mut line = Vec::new();
    let mut total = 0u64;
    let mut partial = false;
    loop {
        let chunk = input.fill_buf()?;
        if chunk.is_empty() {
            break;
        }
        for segment in chunk.split_inclusive(|b| *b == b'\n') {
            let body = segment.strip_suffix(b"\n");
            let number = total + 1;
            let selected = (offset..=end).contains(&number);
            if selected {
                let bytes = body.unwrap_or(segment);
                line.extend_from_slice(&bytes[..bytes.len().min(cap - line.len())]);
            }
            partial = body.is_none();
            if !partial {
                if selected {
                    page.line(&line, number, grant);
                }
                line.clear();
                total = number;
            }
        }
        let n = chunk.len();
        input.consume(n);
    }
    if partial {
        total += 1;
        if (offset..=end).contains(&total) {
            page.line(&line, total, grant);
        }
    }
    if matches!(format, Format::Utf16(_)) {
        total += u64::from(!partial);
        if page.kept == 0 {
            page.line(b"", offset, grant);
        }
    }
    let mut result = blank(grant.identity.size);
    result["content"] = json!(page.content);
    result["total_lines"] = json!(total);
    if matches!(format, Format::Notebook) {
        result["extracted_document"] = json!(true);
    } else if page.shortened {
        result["truncated_lines"] = json!(true);
    }
    let mut hint = match format {
        Format::Utf16(label) => format!(
            "Transcoded from {label} to UTF-8 for display. Text edits via patch/write_file would re-encode as UTF-8."
        ),
        Format::Text if total == 0 => "File is empty (0 bytes).".into(),
        Format::Text if offset > total => format!(
            "Note: offset {offset} is beyond the end of the file ({total} lines total). Retry with offset <= {total}."
        ),
        _ => String::new(),
    };
    if total > end {
        result["truncated"] = json!(true);
        if !hint.is_empty() {
            hint.push(' ');
        }
        hint.push_str(&format!(
            "Use offset={} to continue reading (showing {offset}-{end} of {total} lines)",
            end + 1
        ));
    }
    if !hint.is_empty() {
        result["hint"] = json!(hint);
    }
    if !matches!(format, Format::Notebook) {
        for field in ["is_binary", "is_image", "not_found"] {
            result[field] = json!(false);
        }
    }
    if page.full {
        let next = offset.saturating_add(page.kept);
        result["truncated"] = json!(true);
        result["truncated_by"] = json!("bytes");
        result["next_offset"] = json!(next);
        let mut hint = format!(
            "Output truncated at the {}-char read budget after {} line(s) (showing lines {offset}-{} of {total}). Use offset={next} to continue.",
            grouped(grant.max_chars),
            page.kept,
            next.saturating_sub(1)
        );
        if page
            .content
            .split('\n')
            .next()
            .is_some_and(|s| s.chars().count() >= grant.max_chars)
        {
            result["truncated_lines"] = json!(true);
            hint.push_str(" Note: the first line alone exceeded the budget and was clamped mid-line; its remainder is not retrievable via offset.");
        }
        result["hint"] = json!(hint);
    }
    Ok(result)
}

fn grouped(n: impl ToString) -> String {
    let digits = n.to_string();
    let mut out = String::new();
    for (i, ch) in digits.char_indices() {
        if i != 0 && (digits.len() - i).is_multiple_of(3) {
            out.push(',');
        }
        out.push(ch);
    }
    out
}

fn add_hints(result: &mut Value, limit: u64) {
    let mut opens = 0;
    let mut closes = 0;
    for line in result["content"].as_str().unwrap_or_default().lines() {
        if let Some((_, body)) = line.split_once('|') {
            opens += usize::from(body.starts_with("<<<<<<< "));
            closes += usize::from(body.starts_with(">>>>>>> "));
        }
    }
    let n = opens.min(closes);
    if n > 0 {
        result["conflict_blocks"] = json!(n);
        result["_hint"] = json!(format!(
            "{n} unresolved git merge-conflict block(s) (<<<<<<< / ======= / >>>>>>>) in this range. Resolve them (keep one side or combine, delete the markers) before editing around them."
        ));
    } else if result["truncated"] == true
        && limit > 200
        && let Some(size) = result["file_size"].as_u64().filter(|s| *s > 512000)
    {
        result["_hint"] = json!(format!(
            "This file is large ({} bytes). Consider reading only the section you need with offset and limit to keep context usage efficient.",
            grouped(size)
        ));
    }
}
