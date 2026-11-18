package polyglot

// rustManifest is the Cargo manifest of the generated worker crate. The fence
// source is the crate root, so a fence uses serde exactly as any Rust program
// does (`use serde::{Serialize, Deserialize}` + `#[derive(...)]`); the
// dependencies are the standard serde toolchain, never a Bash#-specific
// carrier type. `[workspace]` keeps a temp directory under a user's checkout
// from being captured by that checkout's workspace. The binary name is unique
// per source (%s) so concurrent builds sharing one target directory never
// overwrite each other's output.
const rustManifest = `[package]
name = "bashpp_module"
version = "0.0.0"
edition = "2024"

[workspace]

[[bin]]
name = "%s"
path = "src/main.rs"

[dependencies]
serde = { version = "1", features = ["derive"] }
serde_json = "1"
base64 = "0.22"

[profile.dev]
opt-level = 0
debug = false
`

// rustWorkerRuntime is appended after a fence's own source. It turns the crate
// into the persistent worker Module.ensure launches: one JSON request per
// stdin line, one JSON response per line on fd 3 (Unix) or on the original
// stdout handle behind the "\x1eBASHPP" marker (Windows, where the parent
// cannot pass a fourth file). Values are serde_json::Value, so an ordinary
// `#[derive(Serialize, Deserialize)]` struct or Vec<Struct> crosses the Bash#
// Object mapping through serde_json::from_value / to_value; the generated
// __bpp_dispatch (see rustWorkerSource) is the only per-fence code.
//
// Island stdout/stderr are redirected to temporary files for the duration of
// each call and returned in the response, so the shell replays them and the
// protocol stream stays clean. Unix uses dup/dup2 on fds 1 and 2; Windows
// uses SetStdHandle, which Rust's std consults on every stdout/stderr write.
//
// Two boundary types are published to the fence as `bashpp::Handle<T>` and
// `bashpp::Callback`. A Handle is an opaque token for a value the worker owns
// for its lifetime: it crosses as the value adapter's `{"$handle": …}` shape,
// it is validated (existence and type) when an argument deserializes, and it
// is freed exactly once by the host's `release` operation or by `take`. A
// Callback is a shell function the host passed with the call: `call` writes
// the target-invocation request (`{"call":"shell","op":"call",…}`) on the
// protocol stream and reads the reply from stdin, serving any request the
// host nests in the meantime, so re-entry is strictly last-in-first-out on
// the one existing channel pair and bounded by MAX_DEPTH.
const rustWorkerRuntime = `
// ---- Bash# Rust worker runtime (generated; do not edit) ----
mod bashpp {
    pub use crate::__bpp::{Callback, Handle};
}

mod __bpp {
    use serde_json::Value;
    use std::any::{Any, TypeId};
    use std::cell::{Cell, RefCell};
    use std::collections::HashMap;
    use std::io::{Read, Seek, Write};
    use std::marker::PhantomData;
    use std::rc::Rc;

    /// Nested call requests the worker serves while a callback reply is
    /// pending. The host bounds callback re-entry first; this is the safety
    /// net for a host that does not.
    pub const MAX_DEPTH: usize = 16;

    pub type Dispatch = fn(&str, &[Value]) -> Result<Value, String>;

    #[derive(serde::Deserialize)]
    pub struct Request {
        #[serde(default)]
        pub id: u64,
        pub op: String,
        #[serde(default)]
        pub name: String,
        #[serde(default)]
        pub args: Vec<Value>,
        #[serde(default)]
        pub handle: u64,
        #[serde(default)]
        pub iterator: u64,
        #[serde(default)]
        pub pgid: i32,
    }

    struct Entry {
        value: Rc<dyn Any>,
        type_id: TypeId,
        type_name: &'static str,
    }

    struct Capture {
        out: std::fs::File,
        out_path: std::path::PathBuf,
        out_pos: u64,
        err: std::fs::File,
        err_path: std::path::PathBuf,
        err_pos: u64,
    }

    thread_local! {
        static ITERATORS: RefCell<HashMap<u64, Box<dyn Iterator<Item=Result<Value,String>>>> > = RefCell::new(HashMap::new());
        static NEXT_ITERATOR: Cell<u64> = const { Cell::new(0) };
        static HANDLES: RefCell<HashMap<u64, Entry>> = RefCell::new(HashMap::new());
        static NEXT_HANDLE: Cell<u64> = const { Cell::new(0) };
        static NEXT_CALLBACK: Cell<u64> = const { Cell::new(0) };
        static DEPTH: Cell<usize> = const { Cell::new(0) };
        static SEQ: Cell<u64> = const { Cell::new(0) };
        static DISPATCH: Cell<Option<Dispatch>> = const { Cell::new(None) };
        static PROTOCOL: RefCell<Option<std::fs::File>> = const { RefCell::new(None) };
        static CAPTURES: RefCell<Vec<Capture>> = const { RefCell::new(Vec::new()) };
    }

    /// An opaque token for a value this worker owns. Copying the token never
    /// copies the value; the value lives until the host releases it, the
    /// fence takes it, or the worker exits.
    pub struct Handle<T: 'static> {
        id: u64,
        _marker: PhantomData<fn() -> T>,
    }
    impl<T: 'static> Clone for Handle<T> {
        fn clone(&self) -> Self { *self }
    }
    impl<T: 'static> Copy for Handle<T> {}
    impl<T: 'static> std::fmt::Debug for Handle<T> {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            write!(f, "Handle<{}>({})", short_type(std::any::type_name::<T>()), self.id)
        }
    }

    /// The type's spelling without module paths: "Counter", "Vec<String>".
    fn short_type(name: &str) -> String {
        let mut out = String::new();
        let mut word = String::new();
        let mut words: Vec<String> = Vec::new();
        for c in name.chars() {
            if c.is_alphanumeric() || c == '_' || c == ':' {
                word.push(c);
            } else {
                if !word.is_empty() {
                    words.push(std::mem::take(&mut word));
                }
                words.push(c.to_string());
            }
        }
        if !word.is_empty() {
            words.push(word);
        }
        for w in words {
            match w.rsplit("::").next() {
                Some(last) => out.push_str(last),
                None => out.push_str(&w),
            }
        }
        out
    }

    pub fn iterator(value: impl Iterator<Item=Result<Value,String>> + 'static) -> Value {
        let id=NEXT_ITERATOR.with(|n| {n.set(n.get()+1);n.get()});
        ITERATORS.with(|items| items.borrow_mut().insert(id,Box::new(value)));
        serde_json::json!(id)
    }
    fn iterator_next(id: u64) -> Result<Value,String> {
        // Remove while advancing: callbacks may re-enter without borrowing the
        // registry through user code. The iterator is restored only when live.
        let mut item=ITERATORS.with(|items| items.borrow_mut().remove(&id)).ok_or_else(|| "stale Rust iterator".to_string())?;
        match item.next() {
            Some(Ok(value)) => {ITERATORS.with(|items| items.borrow_mut().insert(id,item)); Ok(serde_json::json!({"done":false,"value":value}))},
            Some(Err(error)) => Err(error),
            None => Ok(serde_json::json!({"done":true})),
        }
    }
    fn stale(id: u64) -> String {
        format!("stale Rust handle {}: released or never created", id)
    }

    impl<T: 'static> Handle<T> {
        /// Registers value with the worker and returns its token.
        pub fn new(value: T) -> Handle<T> {
            let id = NEXT_HANDLE.with(|next| {
                next.set(next.get() + 1);
                next.get()
            });
            HANDLES.with(|handles| {
                handles.borrow_mut().insert(id, Entry {
                    value: Rc::new(RefCell::new(value)),
                    type_id: TypeId::of::<T>(),
                    type_name: std::any::type_name::<T>(),
                });
            });
            Handle { id, _marker: PhantomData }
        }
        pub fn id(&self) -> u64 { self.id }
        fn cell(&self) -> Result<Rc<dyn Any>, String> {
            HANDLES.with(|handles| {
                let handles = handles.borrow();
                let entry = handles.get(&self.id).ok_or_else(|| stale(self.id))?;
                if entry.type_id != TypeId::of::<T>() {
                    return Err(format!("Rust handle {} is a {}, not a {}", self.id, short_type(entry.type_name), short_type(std::any::type_name::<T>())));
                }
                Ok(entry.value.clone())
            })
        }
        /// Borrows the value for the duration of f. Fails when a call still in
        /// progress holds the value mutably (a callback that re-entered the
        /// worker), never panics.
        pub fn with<R>(&self, f: impl FnOnce(&T) -> R) -> Result<R, String> {
            let rc = self.cell()?;
            let cell = (&*rc as &dyn Any).downcast_ref::<RefCell<T>>().ok_or_else(|| stale(self.id))?;
            let value = cell.try_borrow().map_err(|_| format!("Rust handle {} is mutably borrowed by a call still in progress", self.id))?;
            Ok(f(&value))
        }
        /// Mutably borrows the value for the duration of f. Fails when a call
        /// still in progress holds the value, never panics.
        pub fn with_mut<R>(&self, f: impl FnOnce(&mut T) -> R) -> Result<R, String> {
            let rc = self.cell()?;
            let cell = (&*rc as &dyn Any).downcast_ref::<RefCell<T>>().ok_or_else(|| stale(self.id))?;
            let mut value = cell.try_borrow_mut().map_err(|_| format!("Rust handle {} is borrowed by a call still in progress", self.id))?;
            Ok(f(&mut value))
        }
        /// Releases the token and returns the value: the fence-side release.
        /// Fails, leaving the value registered, when a call still in progress
        /// borrows it.
        pub fn take(self) -> Result<T, String> {
            let entry = HANDLES.with(|handles| handles.borrow_mut().remove(&self.id)).ok_or_else(|| stale(self.id))?;
            if entry.type_id != TypeId::of::<T>() {
                let message = format!("Rust handle {} is a {}, not a {}", self.id, short_type(entry.type_name), short_type(std::any::type_name::<T>()));
                HANDLES.with(|handles| handles.borrow_mut().insert(self.id, entry));
                return Err(message);
            }
            let Entry { value, type_id, type_name } = entry;
            let rc = match Rc::downcast::<RefCell<T>>(value) {
                Ok(rc) => rc,
                Err(value) => {
                    HANDLES.with(|handles| handles.borrow_mut().insert(self.id, Entry { value, type_id, type_name }));
                    return Err(stale(self.id));
                }
            };
            match Rc::try_unwrap(rc) {
                Ok(cell) => Ok(cell.into_inner()),
                Err(rc) => {
                    HANDLES.with(|handles| handles.borrow_mut().insert(self.id, Entry { value: rc, type_id, type_name }));
                    Err(format!("Rust handle {} is borrowed by a call still in progress", self.id))
                }
            }
        }
        /// Releases the token and drops the value.
        pub fn release(self) -> Result<(), String> {
            self.take().map(drop)
        }
    }

    fn handle_id(value: &Value) -> Option<u64> {
        let map = value.as_object()?;
        if map.len() != 1 {
            return None;
        }
        match map.get("$handle")? {
            Value::Number(id) => id.as_u64(),
            Value::Object(inner) => inner.get("id")?.as_u64(),
            _ => None,
        }
    }

    impl<T: 'static> serde::Serialize for Handle<T> {
        fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            let name = HANDLES.with(|handles| handles.borrow().get(&self.id).map(|entry| short_type(entry.type_name)))
                .unwrap_or_else(|| short_type(std::any::type_name::<T>()));
            serde_json::json!({"$handle": {"id": self.id, "type": name}}).serialize(serializer)
        }
    }
    impl<'de, T: 'static> serde::Deserialize<'de> for Handle<T> {
        fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
            let value = Value::deserialize(deserializer)?;
            let id = handle_id(&value).ok_or_else(|| serde::de::Error::custom(format!("expected a Rust handle, got {}", value)))?;
            let handle = Handle { id, _marker: PhantomData };
            handle.cell().map_err(serde::de::Error::custom)?;
            Ok(handle)
        }
    }

    /// Releases a handle on the host's behalf; the value is dropped exactly
    /// once, when the last borrow of a call still in progress ends.
    pub fn release_id(id: u64) -> Result<(), String> {
        HANDLES.with(|handles| handles.borrow_mut().remove(&id)).map(drop).ok_or_else(|| stale(id))
    }

    /// A shell function the host passed with the call that is in flight. It
    /// expires with that call: the host refuses a token invoked later.
    #[derive(Clone, Copy, Debug)]
    pub struct Callback {
        id: u64,
    }
    impl serde::Serialize for Callback {
        fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            serde_json::json!({"$callback": self.id}).serialize(serializer)
        }
    }
    impl<'de> serde::Deserialize<'de> for Callback {
        fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
            let value = Value::deserialize(deserializer)?;
            let id = value.as_object().filter(|map| map.len() == 1).and_then(|map| map.get("$callback")).and_then(|id| id.as_u64());
            match id {
                Some(id) => Ok(Callback { id }),
                None => Err(serde::de::Error::custom(format!("expected a shell callback, got {}", value))),
            }
        }
    }
    impl Callback {
        /// Invokes the shell function synchronously and returns its result.
        /// Island output written so far is delivered to the shell first, so
        /// it appears before whatever the callback prints.
        pub fn call(&self, args: Vec<Value>) -> Result<Value, String> {
            let (out, err) = flush_capture();
            let id = NEXT_CALLBACK.with(|next| {
                next.set(next.get() + 1);
                next.get()
            });
            write_frame(&serde_json::json!({"id": id, "call": "shell", "op": "call", "callback": self.id, "args": args, "stdout": out, "stderr": err}))?;
            loop {
                let line = read_line()?;
                if line.trim().is_empty() {
                    continue;
                }
                let value: Value = serde_json::from_str(&line).map_err(|error| format!("invalid shell callback reply: {}", error))?;
                if value.get("op").is_some() {
                    write_frame(&respond_line(&line))?;
                    continue;
                }
                if value.get("id").and_then(|got| got.as_u64()) != Some(id) {
                    return Err(format!("shell callback reply id {} does not match request id {}", value.get("id").cloned().unwrap_or(Value::Null), id));
                }
                if value.get("ok").and_then(|ok| ok.as_bool()) == Some(true) {
                    return Ok(value.get("result").cloned().unwrap_or(Value::Null));
                }
                return Err(match value.get("error") {
                    Some(Value::String(message)) => message.clone(),
                    Some(Value::Object(detail)) => detail.get("message").and_then(|m| m.as_str()).unwrap_or("shell callback failed").to_string(),
                    _ => "shell callback failed".to_string(),
                });
            }
        }
        /// Invokes the shell function and deserializes its result.
        pub fn call_as<T: serde::de::DeserializeOwned>(&self, args: Vec<Value>) -> Result<T, String> {
            serde_json::from_value(self.call(args)?).map_err(|error| format!("shell callback result: {}", error))
        }
    }

    pub fn arity(func: &str, args: &[Value], want: usize) -> Result<(), String> {
        if args.len() != want {
            return Err(format!("Rust function {} expects {} arguments, got {}", func, want, args.len()));
        }
        Ok(())
    }
    pub fn arg<T: serde::de::DeserializeOwned>(func: &str, args: &[Value], i: usize) -> Result<T, String> {
        serde_json::from_value(args[i].clone()).map_err(|e| format!("Rust function {} argument {}: {}", func, i + 1, e))
    }
    pub fn bytes_arg(func: &str, args: &[Value], i: usize) -> Result<Vec<u8>, String> {
        use base64::Engine;
        if let Value::Object(map) = &args[i] {
            if let (1, Some(Value::String(text))) = (map.len(), map.get("$bytes")) {
                return base64::engine::general_purpose::STANDARD
                    .decode(text)
                    .map_err(|e| format!("Rust function {} argument {}: {}", func, i + 1, e));
            }
        }
        Err(format!("Rust function {} argument {}: expected bytes", func, i + 1))
    }
    pub fn bytes_value(data: &[u8]) -> Value {
        use base64::Engine;
        serde_json::json!({"$bytes": base64::engine::general_purpose::STANDARD.encode(data)})
    }
    pub fn value<T: serde::Serialize>(func: &str, value: T) -> Result<Value, String> {
        serde_json::to_value(value).map_err(|e| format!("Rust function {} result: {}", func, e))
    }

    #[cfg(unix)]
    mod os {
        unsafe extern "C" {
            fn dup(fd: i32) -> i32;
            fn dup2(old: i32, new: i32) -> i32;
            fn close(fd: i32) -> i32;
        }
        pub type Saved = (i32, i32);
        pub const MARKER: bool = false;
        pub fn protocol() -> std::fs::File {
            use std::os::unix::io::FromRawFd;
            unsafe { std::fs::File::from_raw_fd(3) }
        }
        pub fn redirect(out: &std::fs::File, err: &std::fs::File) -> Saved {
            use std::os::unix::io::AsRawFd;
            unsafe {
                let saved = (dup(1), dup(2));
                dup2(out.as_raw_fd(), 1);
                dup2(err.as_raw_fd(), 2);
                saved
            }
        }
        pub fn restore(saved: Saved) {
            unsafe {
                dup2(saved.0, 1);
                dup2(saved.1, 2);
                close(saved.0);
                close(saved.1);
            }
        }
    }
    #[cfg(windows)]
    mod os {
        use std::os::windows::io::{AsRawHandle, FromRawHandle};
        type Handle = *mut core::ffi::c_void;
        const STD_OUTPUT_HANDLE: u32 = -11i32 as u32;
        const STD_ERROR_HANDLE: u32 = -12i32 as u32;
        #[link(name = "kernel32")]
        unsafe extern "system" {
            fn GetStdHandle(id: u32) -> Handle;
            fn SetStdHandle(id: u32, handle: Handle) -> i32;
        }
        pub type Saved = (Handle, Handle);
        pub const MARKER: bool = true;
        pub fn protocol() -> std::fs::File {
            unsafe { std::fs::File::from_raw_handle(GetStdHandle(STD_OUTPUT_HANDLE)) }
        }
        pub fn redirect(out: &std::fs::File, err: &std::fs::File) -> Saved {
            unsafe {
                let saved = (GetStdHandle(STD_OUTPUT_HANDLE), GetStdHandle(STD_ERROR_HANDLE));
                SetStdHandle(STD_OUTPUT_HANDLE, out.as_raw_handle());
                SetStdHandle(STD_ERROR_HANDLE, err.as_raw_handle());
                saved
            }
        }
        pub fn restore(saved: Saved) {
            unsafe {
                SetStdHandle(STD_OUTPUT_HANDLE, saved.0);
                SetStdHandle(STD_ERROR_HANDLE, saved.1);
            }
        }
    }

    fn temp(seq: u64, tag: &str) -> std::io::Result<(std::fs::File, std::path::PathBuf)> {
        let path = std::env::temp_dir().join(format!("bashpp-rust-{}-{}-{}", std::process::id(), seq, tag));
        let file = std::fs::OpenOptions::new().read(true).write(true).create(true).truncate(true).open(&path)?;
        Ok((file, path))
    }
    /// Reads what fd 1/2 wrote since pos. The redirected fd and the file share
    /// one offset, so reading to the end leaves writes continuing at the end.
    fn since(file: &mut std::fs::File, pos: &mut u64) -> String {
        let mut buf = Vec::new();
        let _ = file.seek(std::io::SeekFrom::Start(*pos));
        let _ = file.read_to_end(&mut buf);
        *pos += buf.len() as u64;
        String::from_utf8_lossy(&buf).into_owned()
    }
    fn drain(mut file: std::fs::File, path: std::path::PathBuf, mut pos: u64) -> String {
        let text = since(&mut file, &mut pos);
        drop(file);
        let _ = std::fs::remove_file(path);
        text
    }
    fn flush_capture() -> (String, String) {
        let _ = std::io::stdout().flush();
        let _ = std::io::stderr().flush();
        CAPTURES.with(|captures| {
            let mut captures = captures.borrow_mut();
            match captures.last_mut() {
                Some(capture) => (since(&mut capture.out, &mut capture.out_pos), since(&mut capture.err, &mut capture.err_pos)),
                None => (String::new(), String::new()),
            }
        })
    }
    fn guarded<F: FnOnce() -> Result<Value, String>>(call: F) -> Result<Value, String> {
        match std::panic::catch_unwind(std::panic::AssertUnwindSafe(call)) {
            Ok(result) => result,
            Err(payload) => Err(match payload.downcast_ref::<&str>() {
                Some(text) => format!("Rust function panicked: {}", text),
                None => match payload.downcast_ref::<String>() {
                    Some(text) => format!("Rust function panicked: {}", text),
                    None => "Rust function panicked".to_string(),
                },
            }),
        }
    }
    fn envelope_error(code: &str, message: String) -> Value {
        serde_json::json!({"code": code, "message": message})
    }
    pub fn capture<F: FnOnce() -> Result<Value, String>>(seq: u64, call: F) -> (Result<Value, String>, String, String) {
        let (out, out_path, err, err_path) = match (temp(seq, "out"), temp(seq, "err")) {
            (Ok((out, out_path)), Ok((err, err_path))) => (out, out_path, err, err_path),
            _ => return (guarded(call), String::new(), String::new()),
        };
        let _ = std::io::stdout().flush();
        let _ = std::io::stderr().flush();
        let saved = os::redirect(&out, &err);
        CAPTURES.with(|captures| captures.borrow_mut().push(Capture { out, out_path, out_pos: 0, err, err_path, err_pos: 0 }));
        let result = guarded(call);
        let _ = std::io::stdout().flush();
        let _ = std::io::stderr().flush();
        os::restore(saved);
        let capture = CAPTURES.with(|captures| captures.borrow_mut().pop());
        match capture {
            Some(capture) => (result, drain(capture.out, capture.out_path, capture.out_pos), drain(capture.err, capture.err_path, capture.err_pos)),
            None => (result, String::new(), String::new()),
        }
    }

    fn read_line() -> Result<String, String> {
        let mut line = String::new();
        match std::io::stdin().read_line(&mut line) {
            Ok(0) => Err("host closed the protocol".to_string()),
            Ok(_) => Ok(line),
            Err(error) => Err(format!("protocol read: {}", error)),
        }
    }
    fn write_frame(value: &Value) -> Result<(), String> {
        PROTOCOL.with(|protocol| {
            let mut protocol = protocol.borrow_mut();
            let file = protocol.as_mut().ok_or_else(|| "protocol not open".to_string())?;
            let mut frame = String::new();
            if os::MARKER {
                frame.push_str("\u{1e}BASHPP");
            }
            frame.push_str(&value.to_string());
            frame.push('\n');
            file.write_all(frame.as_bytes()).and_then(|_| file.flush()).map_err(|error| format!("protocol write: {}", error))
        })
    }
    fn respond_line(line: &str) -> Value {
        match serde_json::from_str::<Request>(line) {
            Err(error) => serde_json::json!({"id": 0, "ok": false, "error": envelope_error("RUST-EWORKER-REQUEST", format!("invalid request: {}", error)), "stdout": "", "stderr": ""}),
            Ok(request) => respond(request),
        }
    }
    #[cfg(unix)]
    fn process_group(group: i32) -> Result<i32,String> {
        unsafe extern "C" { fn setpgid(pid:i32,pgid:i32)->i32; fn getpgrp()->i32; }
        if unsafe{setpgid(0,group)} != 0 { return Err(std::io::Error::last_os_error().to_string()) }
        Ok(unsafe{getpgrp()})
    }
    #[cfg(not(unix))]
    fn process_group(_group: i32) -> Result<i32,String> {Ok(std::process::id() as i32)}
    fn respond(request: Request) -> Value {
        match request.op.as_str() {
            "job_join" | "job_leave" => {
                match process_group(request.pgid) {
                    Ok(group) => serde_json::json!({"id":request.id,"ok":true,"result":group}),
                    Err(message) => serde_json::json!({"id":request.id,"ok":false,"error":envelope_error("RUST-EPGID",message)}),
                }
            }
            "load" => serde_json::json!({"id": request.id, "ok": true, "result": null, "stdout": "", "stderr": ""}),
            "call" | "iter_open" => {
                let depth = DEPTH.with(|depth| depth.get());
                if depth >= MAX_DEPTH {
                    return serde_json::json!({"id": request.id, "ok": false, "error": envelope_error("RUST-EWORKER-DEPTH", format!("shell callback re-entry exceeds the worker bound of {}", MAX_DEPTH)), "stdout": "", "stderr": ""});
                }
                let dispatch = match DISPATCH.with(|dispatch| dispatch.get()) {
                    Some(dispatch) => dispatch,
                    None => return serde_json::json!({"id": request.id, "ok": false, "error": envelope_error("RUST-EWORKER-OP", "worker not serving".to_string()), "stdout": "", "stderr": ""}),
                };
                let seq = SEQ.with(|seq| {
                    seq.set(seq.get() + 1);
                    seq.get()
                });
                DEPTH.with(|d| d.set(depth + 1));
                let (result, out, err) = capture(seq, || dispatch(&request.name, &request.args));
                DEPTH.with(|d| d.set(depth));
                match result {
                    Ok(value) => serde_json::json!({"id": request.id, "ok": true, "result": value, "stdout": out, "stderr": err}),
                    Err(message) => serde_json::json!({"id": request.id, "ok": false, "error": envelope_error("RUST-ECALL", message), "stdout": out, "stderr": err}),
                }
            }
            "iter_next" | "iter_close" => {
                let (result,out,err)=capture(request.id, || {
                    if request.op == "iter_next" { iterator_next(request.iterator) }
                    else { ITERATORS.with(|items| items.borrow_mut().remove(&request.iterator)); Ok(Value::Null) }
                });
                match result {
                    Ok(value) => serde_json::json!({"id":request.id,"ok":true,"result":value,"stdout":out,"stderr":err}),
                    Err(message) => serde_json::json!({"id":request.id,"ok":false,"error":envelope_error("RUST-EITERATOR",message),"stdout":out,"stderr":err}),
                }
            }
            "release" => match release_id(request.handle) {
                Ok(()) => serde_json::json!({"id": request.id, "ok": true, "result": null, "stdout": "", "stderr": ""}),
                Err(message) => serde_json::json!({"id": request.id, "ok": false, "error": envelope_error("RUST-EHANDLE", message), "stdout": "", "stderr": ""}),
            },
            other => serde_json::json!({"id": request.id, "ok": false, "error": envelope_error("RUST-EWORKER-OP", format!("unknown operation {}", other)), "stdout": "", "stderr": ""}),
        }
    }

    pub fn serve(dispatch: Dispatch) {
        std::panic::set_hook(Box::new(|_| {}));
        DISPATCH.with(|slot| slot.set(Some(dispatch)));
        PROTOCOL.with(|protocol| *protocol.borrow_mut() = Some(os::protocol()));
        loop {
            let line = match read_line() {
                Ok(line) => line,
                Err(_) => break,
            };
            if line.trim().is_empty() {
                continue;
            }
            if write_frame(&respond_line(&line)).is_err() {
                break;
            }
        }
        // fd 3 (or the inherited stdout handle) belongs to the parent; never
        // close it from a thread-local destructor.
        PROTOCOL.with(|protocol| {
            if let Some(file) = protocol.borrow_mut().take() {
                std::mem::forget(file);
            }
        });
    }
}

fn main() {
    __bpp::serve(__bpp_dispatch);
}
`
