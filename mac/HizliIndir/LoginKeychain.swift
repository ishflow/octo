import Foundation
import Security

/// Site passwords live only here (generic password, account = host pattern); they are never sent to the engine.
enum LoginKeychain {
    static let service = "com.uba.hizliindir.login"  // pre-Octo name kept: renaming would orphan saved site passwords

    private static func query(_ host: String) -> [CFString: Any] {
        [kSecClass: kSecClassGenericPassword, kSecAttrService: service, kSecAttrAccount: host]
    }

    static func password(for host: String) -> String? {
        var q = query(host)
        q[kSecReturnData] = true
        q[kSecMatchLimit] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let d = out as? Data else { return nil }
        return String(data: d, encoding: .utf8)
    }

    /// Adds or replaces. Returns a Turkish message on failure.
    /// Re-created, not updated: an update keeps the old access list, so after a new engine build (new code hash)
    /// the engine would keep prompting; saving the login again re-trusts the current `octo`.
    static func set(_ password: String, for host: String, read: (String) -> String? = password(for:),
                    add: (CFDictionary) -> OSStatus = { SecItemAdd($0, nil) }) -> String? {
        let old = read(host)
        SecItemDelete(query(host) as CFDictionary)
        var item = query(host)
        item[kSecValueData] = Data(password.utf8)
        if let a = access(trusting: [Bundle.main.url(forAuxiliaryExecutable: "octo")?.path].compactMap { $0 }) {
            item[kSecAttrAccess] = a
        }
        let st = add(item as CFDictionary)
        guard st != errSecSuccess else { return nil }
        // The delete already happened: put the previous password back (without the access list that just failed) so downloads keep logging in.
        if let old {
            var back = query(host)
            back[kSecValueData] = Data(old.utf8)
            _ = add(back as CFDictionary)
        }
        return "Parola Anahtar Zinciri'ne kaydedilemedi (\(st))."
    }

    /// Octo itself plus `paths` may read the item without a prompt.
    static func access(trusting paths: [String]) -> SecAccess? {
        let apps = ([nil] + paths).compactMap { p -> SecTrustedApplication? in
            var app: SecTrustedApplication?
            return SecTrustedApplicationCreateFromPath(p, &app) == errSecSuccess ? app : nil  // nil path = this app
        }
        var access: SecAccess?
        return SecAccessCreate("Octo" as CFString, apps as CFArray, &access) == errSecSuccess ? access : nil
    }

    static func delete(host: String) { SecItemDelete(query(host) as CFDictionary) }
}
