import Flutter
import Foundation
import Security

/// 设备密钥：Secure Enclave 中的 P-256，私钥不可导出。
/// sign 接收 SHA-256 摘要，使用 ecdsaSignatureDigestX962SHA256 直接签摘要（DER 输出）。
final class DeviceKeyChannel {
    static let name = "yuhai/device_key"
    private let tag = "com.yuhai.device.p256".data(using: .utf8)!

    func handle(_ call: FlutterMethodCall, result: @escaping FlutterResult) {
        do {
            switch call.method {
            case "publicKey":
                result(try publicKeySpkiBase64())
            case "sign":
                guard let args = call.arguments as? [String: Any], let digest = args["digest"] as? FlutterStandardTypedData else {
                    result(FlutterError(code: "ARG", message: "digest required", details: nil)); return
                }
                result(FlutterStandardTypedData(bytes: try sign(digest.data)))
            case "regenerate":
                deleteKey(); _ = try generate(); result(nil)
            default:
                result(FlutterMethodNotImplemented)
            }
        } catch {
            result(FlutterError(code: "DEVICE_KEY", message: "Device key operation failed", details: nil))
        }
    }

    private func loadKey() -> SecKey? {
        let query: [String: Any] = [kSecClass as String: kSecClassKey, kSecAttrApplicationTag as String: tag,
                                    kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom, kSecReturnRef as String: true]
        var item: CFTypeRef?
        return SecItemCopyMatching(query as CFDictionary, &item) == errSecSuccess ? (item as! SecKey) : nil
    }

    private func generate() throws -> SecKey {
        var error: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, .privateKeyUsage, &error) else {
            throw error!.takeRetainedValue()
        }
        var attrs: [String: Any] = [
            kSecAttrKeyType as String: kSecAttrKeyTypeECSECPrimeRandom,
            kSecAttrKeySizeInBits as String: 256,
            kSecPrivateKeyAttrs as String: [kSecAttrIsPermanent as String: true, kSecAttrApplicationTag as String: tag,
                                            kSecAttrAccessControl as String: access],
        ]
        #if !targetEnvironment(simulator)
        attrs[kSecAttrTokenID as String] = kSecAttrTokenIDSecureEnclave
        #endif
        guard let key = SecKeyCreateRandomKey(attrs as CFDictionary, &error) else { throw error!.takeRetainedValue() }
        return key
    }

    private func key() throws -> SecKey { try loadKey() ?? generate() }

    private func publicKeySpkiBase64() throws -> String {
        var error: Unmanaged<CFError>?
        guard let pub = SecKeyCopyPublicKey(try key()),
              let raw = SecKeyCopyExternalRepresentation(pub, &error) as Data? else { throw error!.takeRetainedValue() }
        // iOS 导出的是 X9.63 未压缩点（04||X||Y），补上 P-256 的 SPKI 头得到与 Android 相同的 DER
        let header: [UInt8] = [0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x02, 0x01,
                               0x06, 0x08, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00]
        return (Data(header) + raw).base64EncodedString()
    }

    private func sign(_ digest: Data) throws -> Data {
        guard digest.count == 32 else { throw VpnKeyStore.Failure.invalid }
        var error: Unmanaged<CFError>?
        guard let sig = SecKeyCreateSignature(try key(), .ecdsaSignatureDigestX962SHA256, digest as CFData, &error) as Data? else {
            throw error!.takeRetainedValue()
        }
        return sig
    }

    private func deleteKey() {
        let query: [String: Any] = [kSecClass as String: kSecClassKey, kSecAttrApplicationTag as String: tag]
        SecItemDelete(query as CFDictionary)
    }
}
