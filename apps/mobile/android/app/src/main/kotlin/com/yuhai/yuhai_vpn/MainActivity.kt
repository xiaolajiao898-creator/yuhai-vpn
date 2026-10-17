package com.yuhai.yuhai_vpn

import android.content.Intent
import io.flutter.plugin.common.EventChannel
import io.flutter.embedding.android.FlutterFragmentActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel

class MainActivity : FlutterFragmentActivity() {
    private var vpn: WireGuardChannel? = null
    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        val deviceKey = DeviceKeyChannel(applicationContext)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, DeviceKeyChannel.CHANNEL)
            .setMethodCallHandler { call, result -> deviceKey.handle(call, result) }
        val channel = WireGuardChannel(this)
        vpn = channel
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "yuhai/vpn").setMethodCallHandler(channel)
        EventChannel(flutterEngine.dartExecutor.binaryMessenger, "yuhai/vpn/events").setStreamHandler(channel)
    }
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (vpn?.onActivityResult(requestCode, resultCode) != true) super.onActivityResult(requestCode, resultCode, data)
    }
    override fun onDestroy() { vpn?.dispose(); vpn = null; super.onDestroy() }
}
