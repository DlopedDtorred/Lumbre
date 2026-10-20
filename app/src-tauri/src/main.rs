#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::{
    net::{SocketAddr, TcpStream},
    process::{Child, Command, Stdio},
    sync::Mutex,
    thread,
    time::{Duration, Instant},
};
use tauri::Manager;

struct Backend(Mutex<Option<Child>>);

fn backend_ready() -> bool {
    let address: SocketAddr = "127.0.0.1:11435".parse().expect("valid local address");
    TcpStream::connect_timeout(&address, Duration::from_millis(150)).is_ok()
}

fn start_backend() -> Result<Option<Child>, String> {
    if backend_ready() {
        return Ok(None);
    }
    let mut child = Command::new("lumbre")
        .arg("serve")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|error| format!("No se pudo iniciar `lumbre serve`: {error}. Instala Lumbre y añádelo a PATH."))?;
    let deadline = Instant::now() + Duration::from_secs(12);
    while Instant::now() < deadline {
        if backend_ready() {
            return Ok(Some(child));
        }
        if let Some(status) = child.try_wait().map_err(|error| format!("No se pudo comprobar el servidor Lumbre: {error}"))? {
            return Err(format!("El servidor Lumbre terminó al arrancar ({status})."));
        }
        thread::sleep(Duration::from_millis(150));
    }
    let _ = child.kill();
    let _ = child.wait();
    Err("El servidor Lumbre no respondió en 12 segundos.".into())
}

fn main() {
    tauri::Builder::default()
        .setup(|app| {
            let child = start_backend().map_err(std::io::Error::other)?;
            app.manage(Backend(Mutex::new(child)));
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building Lumbre")
        .run(|app, event| {
            if let tauri::RunEvent::Exit = event {
                if let Some(backend) = app.try_state::<Backend>() {
                    if let Ok(mut child) = backend.0.lock() {
                        if let Some(child) = child.as_mut() {
                            let _ = child.kill();
                            let _ = child.wait();
                        }
                    }
                }
            }
        });
}
