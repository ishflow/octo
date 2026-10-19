import Foundation

/// Runs the bundled `install-native-host.sh` (writes native-messaging manifests for Chromium browsers).
enum HostInstaller {
    struct Result: Sendable { var ok: Bool; var output: String }

    /// Runs the copies bundled in the app (onboarding and Settings > Tarayıcı).
    static func installBundled() async -> Result {
        guard let script = Bundle.main.url(forResource: "install-native-host", withExtension: "sh"),
              let bin = Bundle.main.url(forAuxiliaryExecutable: "octo") else {
            return Result(ok: false, output: "Kurulum dosyaları uygulama paketinde bulunamadı.")
        }
        return await run(script: script, binary: bin)
    }

    /// `home` overrides HOME for the script (tests use a temp dir; the app passes the real one).
    static func run(script: URL, binary: URL, home: String = NSHomeDirectory()) async -> Result {
        await Task.detached {
            let p = Process()
            p.executableURL = URL(fileURLWithPath: "/bin/bash")  // run via bash: bundle copies may lose the exec bit
            p.arguments = [script.path, binary.path]
            p.environment = ["HOME": home, "PATH": "/usr/bin:/bin"]
            let pipe = Pipe()
            p.standardOutput = pipe
            p.standardError = pipe
            do { try p.run() } catch { return Result(ok: false, output: "Betik çalıştırılamadı: \(error.localizedDescription)") }
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            p.waitUntilExit()
            return Result(ok: p.terminationStatus == 0, output: String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines))
        }.value
    }
}
