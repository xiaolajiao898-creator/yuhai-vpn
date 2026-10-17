package com.yuhai.yuhai_vpn

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import io.flutter.plugin.common.MethodChannel
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.Signature
import java.security.spec.ECGenParameterSpec

/**
 * 设备密钥：P-256，优先 StrongBox，退化到 TEE；私钥不可导出。
 * 签名接口接收 Dart 侧算好的 SHA-256 摘要，使用 NONEwithECDSA 直接签摘要，
 * 与服务端 ecdsa.VerifyASN1(pub, sha256(canonical), sig) 对应。
 */
class DeviceKeyChannel(private val context: Context) {
    companion object {
        const val CHANNEL = "yuhai/device_key"
        private const val ALIAS = "yuhai.device.p256"
    }

    fun handle(call: io.flutter.plugin.common.MethodCall, result: MethodChannel.Result) {
        try {
            when (call.method) {
                "publicKey" -> result.success(publicKey())
                "sign" -> result.success(sign(call.argument<ByteArray>("digest")!!))
                "regenerate" -> { deleteKey(); generate(); result.success(null) }
                else -> result.notImplemented()
            }
        } catch (e: Exception) {
            result.error("DEVICE_KEY", "Device key operation failed", null)
        }
    }

    private fun keyStore() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    private fun publicKey(): String {
        if (!keyStore().containsAlias(ALIAS)) generate()
        val pub = keyStore().getCertificate(ALIAS).publicKey
        return Base64.encodeToString(pub.encoded, Base64.NO_WRAP) // X.509 SubjectPublicKeyInfo DER
    }

    private fun generate() {
        val builder = KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN)
            .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
            .setDigests(KeyProperties.DIGEST_NONE, KeyProperties.DIGEST_SHA256)
        val kpg = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            try {
                kpg.initialize(builder.setIsStrongBoxBacked(true).build())
                kpg.generateKeyPair()
                return
            } catch (_: Exception) { /* 无 StrongBox，退化到 TEE */ }
        }
        kpg.initialize(builder.setIsStrongBoxBacked(false).build())
        kpg.generateKeyPair()
    }

    private fun sign(digest: ByteArray): ByteArray {
        require(digest.size == 32)
        val key = keyStore().getKey(ALIAS, null) as java.security.PrivateKey
        return Signature.getInstance("NONEwithECDSA").run { initSign(key); update(digest); sign() }
    }

    private fun deleteKey() { keyStore().apply { if (containsAlias(ALIAS)) deleteEntry(ALIAS) } }
}
