package com.yuhai.yuhai_vpn

import android.app.Activity
import android.net.VpnService
import android.os.Bundle
import android.os.Process
import android.util.Log
import com.it_nomads.fluttersecurestorage.FlutterSecureStorage

/**
 * 模拟器探测，不在界面里出现，也不接收外部配置。
 * 会话读写走和正式版相同的安全存储；隧道配置是写死的本地黑洞，不含服务器地址。
 */
class VpnProbeActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        when (intent?.getStringExtra("op")) {
            "session-write" -> { sessionWrite(); finish() }
            "session-read" -> { sessionRead(intent?.getStringExtra("phase") ?: ""); finish() }
            "vpn" -> Thread {
                try {
                    vpnStart()
                } finally {
                    runOnUiThread { if (!isFinishing) finish() }
                }
            }.start()
            else -> { log("PROBE_FAIL unknown op"); finish() }
        }
    }

    private fun storage(): FlutterSecureStorage {
        val options = HashMap<String, Any>()
        options["encryptedSharedPreferences"] = "false"
        options["resetOnError"] = "false"
        options["keyCipherAlgorithm"] = "RSA_ECB_PKCS1Padding"
        options["storageCipherAlgorithm"] = "AES_CBC_PKCS7Padding"
        options["sharedPreferencesName"] = ""
        options["preferencesKeyPrefix"] = ""
        return FlutterSecureStorage(applicationContext, options)
    }

    private fun sessionWrite() {
        val store = storage()
        store.write(store.addPrefixToKey("session"), SESSION)
        log("SESSION_WRITE ok")
    }

    private fun sessionRead(phase: String) {
        val store = storage()
        val key = store.addPrefixToKey("session")
        val value = if (store.containsKey(key)) store.read(key) else null
        log(if (value == SESSION) "SESSION_READ present phase=$phase" else "SESSION_READ missing phase=$phase")
    }

    private fun vpnStart() {
        try {
            XrayPolicy.checkConfig(LOCAL_BLACKHOLE)
            if (VpnService.prepare(this) != null) {
                log("VPN_START fail permission")
                return
            }
            XrayTunnel.start(applicationContext, LOCAL_BLACKHOLE)
            log("VPN_START ok pid=${Process.myPid()} running=${XrayTunnel.running}")
        } catch (error: Throwable) {
            NativeFailures.rethrowIfFatal(error)
            log("VPN_START fail ${error.javaClass.simpleName}: ${error.message}")
        }
    }

    private fun log(message: String) {
        Log.i(TAG, message)
    }

    companion object {
        const val TAG = "YuhaiProbe"
        private const val SESSION =
            """{"account_no":"probe","device_id":"probe","access_token":"probe-token","refresh_token":"probe-refresh","invite_code":""}"""

        // 与服务端 TUN 配置同一形状：必须带 name，否则内核会用 netlink 枚举网卡，
        // 普通应用没有这个权限。出口是黑洞，没有服务器地址，也不能当代理监听。
        private const val LOCAL_BLACKHOLE =
            """{"log":{"loglevel":"warning"},"dns":{"queryStrategy":"UseIPv4","servers":["1.1.1.1"]},"inbounds":[{"port":0,"protocol":"tun","settings":{"name":"xray0","mtu":1500},"tag":"tun-in"}],"outbounds":[{"protocol":"blackhole","tag":"proxy"},{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"block"},{"protocol":"dns","tag":"dns-out"}],"routing":{"domainStrategy":"AsIs","rules":[{"type":"field","ip":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","127.0.0.0/8"],"outboundTag":"direct"},{"type":"field","network":"tcp,udp","outboundTag":"proxy"}]},"stats":{},"policy":{"system":{"statsOutboundUplink":true,"statsOutboundDownlink":true}}}"""
    }
}
