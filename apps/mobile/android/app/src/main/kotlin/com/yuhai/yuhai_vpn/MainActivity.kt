package com.yuhai.yuhai_vpn

import android.content.Intent
import android.view.WindowManager
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
        val channel = WireGuardChannel.attach(this)
        vpn = channel
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "yuhai/vpn").setMethodCallHandler(channel)
        EventChannel(flutterEngine.dartExecutor.binaryMessenger, "yuhai/vpn/events").setStreamHandler(object : EventChannel.StreamHandler {
            private var mine: EventChannel.EventSink? = null
            override fun onListen(arguments: Any?, events: EventChannel.EventSink) { mine = events; channel.onListen(arguments, events) }
            override fun onCancel(arguments: Any?) { channel.cancelSink(mine); mine = null }
        })
        // 敏感页面（账户密钥备份）期间禁止截屏/录屏，并在最近任务里隐藏缩略图。
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "yuhai/update")
            .setMethodCallHandler(UpdateInstaller(this))
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "yuhai/window").setMethodCallHandler { call, result ->
            when (call.method) {
                "setSecure" -> {
                    val on = call.arguments as? Boolean
                    if (on == null) result.error("BAD_ARGS", "expected bool", null) else {
                        if (on) window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
                        else window.clearFlags(WindowManager.LayoutParams.FLAG_SECURE)
                        result.success(null)
                    }
                }
                else -> result.notImplemented()
            }
        }
    }
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (vpn?.onActivityResult(requestCode, resultCode) != true) super.onActivityResult(requestCode, resultCode, data)
    }
    // 界面销毁（含从最近任务划掉）不断开 VPN：只解除界面绑定，隧道由 VpnService 继续承载。
    override fun onDestroy() { vpn?.detach(this); vpn = null; super.onDestroy() }
}
