import Foundation
import Security
import Network
import WireGuardKit

enum VpnKeyStore {
    static func key(_ identity: String, create: Bool = false) throws -> PrivateKey {
        guard identity.range(of: "^[A-Za-z0-9_-]{1,64}:[A-Za-z0-9_-]{1,64}$", options: .regularExpression) != nil,
              let group = Bundle.main.object(forInfoDictionaryKey: "YuhaiKeychainGroup") as? String,
              !group.contains("$("), !group.isEmpty else { throw Failure.invalid }
        let query: [String: Any] = [kSecClass as String:kSecClassGenericPassword,
            kSecAttrService as String:"yuhai.wireguard",kSecAttrAccount as String:identity,
            kSecAttrAccessGroup as String:group]
        var found: CFTypeRef?
        let status=SecItemCopyMatching((query.merging([kSecReturnData as String:true],uniquingKeysWith:{$1})) as CFDictionary,&found)
        if status==errSecSuccess,let bytes=found as? Data,let key=PrivateKey(rawValue:bytes) { return key }
        guard status==errSecItemNotFound,create else { throw Failure.unavailable }
        let generated=PrivateKey()
        var insert=query;insert[kSecValueData as String]=generated.rawValue
        insert[kSecAttrAccessible as String]=kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let saved=SecItemAdd(insert as CFDictionary,nil)
        if saved==errSecDuplicateItem { return try key(identity) }
        guard saved==errSecSuccess else { throw Failure.unavailable }
        return generated
    }
    // Revalidate MethodChannel/persisted provider data inside the extension.
    // Do not assume that the Flutter parser was the last writer.
    static func validProfile(_ p: [String:Any], now: Date = Date()) -> Bool {
        let fields: Set<String> = ["identity", "generation", "nodeId", "protocol", "endpoint",
            "serverPublicKey", "address", "dns", "mtu", "expiresAt"]
        guard Set(p.keys) == fields,
              let identity = p["identity"] as? String,
              identity.range(of:"^[A-Za-z0-9_-]{1,64}:[A-Za-z0-9_-]{1,64}$", options:.regularExpression) != nil,
              let gen = p["generation"] as? Int, gen > 0,
              let node = p["nodeId"] as? String, let nodeID = Int64(node), nodeID > 0, String(nodeID) == node,
              p["protocol"] as? String == "WIREGUARD",
              let expiryText = p["expiresAt"] as? String,
              expiryText.range(of:"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\\.[0-9]+)?Z$",options:.regularExpression) != nil,
              let expiry = date(expiryText), expiry > now, expiry.timeIntervalSince(now) <= 305,
              let keyText = p["serverPublicKey"] as? String, let bytes = Data(base64Encoded:keyText),
              bytes.count == 32, bytes.contains(where:{$0 != 0}), bytes.base64EncodedString() == keyText,
              let address = p["address"] as? String, address.hasSuffix("/32"),
              let ipv4 = IPv4Address(String(address.dropLast(3))),
              let dns = p["dns"] as? [String], !dns.isEmpty, dns.count <= 8, Set(dns).count == dns.count,
              dns.allSatisfy(validIP),
              let endpoint = p["endpoint"] as? String, endpoint.count <= 320, Endpoint(from:endpoint) != nil,
              !endpoint.contains(where:{$0.isWhitespace}), !endpoint.contains("@"), !endpoint.contains("/"),
              let mtu = p["mtu"] as? Int, (1280...1500).contains(mtu) else {return false}
        let octets = Array(ipv4.rawValue)
        return octets[0] == 10 || (octets[0] == 172 && (16...31).contains(octets[1])) ||
            (octets[0] == 192 && octets[1] == 168)
    }
    private static func validIP(_ raw: String) -> Bool {
        if let ip = IPv4Address(raw) {let bytes=Array(ip.rawValue);return bytes[0] != 0 && bytes[0] < 224}
        guard raw.range(of:"^[0-9A-Fa-f:]{2,39}$",options:.regularExpression) != nil,
              let ip=IPv6Address(raw) else {return false}
        let bytes=Array(ip.rawValue)
        return bytes.contains(where:{$0 != 0}) && bytes[0] != 255
    }
    static func date(_ raw: String) -> Date? {
        let f=ISO8601DateFormatter();f.formatOptions=[.withInternetDateTime,.withFractionalSeconds]
        guard let parsed=f.date(from:raw) ?? ISO8601DateFormatter().date(from:raw),
              ISO8601DateFormatter().string(from:parsed).prefix(19) == raw.prefix(19) else {return nil}
        return parsed
    }
    enum Failure:Error { case invalid,unavailable }
}
