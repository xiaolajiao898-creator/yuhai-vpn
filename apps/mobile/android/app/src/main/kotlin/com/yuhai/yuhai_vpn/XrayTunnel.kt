package com.yuhai.yuhai_vpn

import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.os.SystemClock
import libv2ray.CoreCallbackHandler
import libv2ray.CoreController
import libv2ray.Libv2ray

/**
 * Xray 内核 + 系统 VPN 接口的生命周期。所有方法都只在 WireGuardChannel 的单线程执行器上调用，
 * 与 WireGuard 隧道的增删串行，天然不会同时存在两条隧道。
 *
 * 路由：IPv4 与 IPv6 全部进入 TUN（与 WireGuard 路径一致，IPv6 不能绕过）；本应用自身排除在外，
 * 内核连出口服务器的流量因此走物理网络而不会回环。DNS 指向 1.1.1.1 / 8.8.8.8，
 * 进入 TUN 后被内核规则截获，经出口以 DoH 解析，不在本地网络明文泄露。
 */
internal object XrayTunnel {
    private val lock = Object()
    private var service: XrayVpnService? = null
    @Volatile private var controller: CoreController? = null
    private var tun: ParcelFileDescriptor? = null
    private var envReady = false
    @Volatile var onRevoked: (() -> Unit)? = null

    val running: Boolean get() = controller?.isRunning == true

    fun attach(s: XrayVpnService) = synchronized(lock) { service = s; lock.notifyAll() }
    fun detach(s: XrayVpnService) = synchronized(lock) { if (service === s) service = null }

    private fun awaitService(timeoutMs: Long): XrayVpnService? = synchronized(lock) {
        val end = SystemClock.elapsedRealtime() + timeoutMs
        while (service == null) {
            val left = end - SystemClock.elapsedRealtime()
            if (left <= 0) break
            lock.wait(left)
        }
        service
    }

    fun start(context: Context, config: String) {
        // 换出口：只停内核与旧接口，沿用仍在运行的服务实例，避免与服务的异步销毁赛跑。
        stopCore()
        if (synchronized(lock) { service } == null) context.startService(Intent(context, XrayVpnService::class.java))
        val svc = awaitService(5000) ?: error("vpn service not started")
        val builder = svc.Builder()
            .setSession("宇海")
            .setMtu(1500)
            .addAddress("172.19.0.1", 30)
            .addAddress("fdfe:dcba:9876::1", 126)
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .addDnsServer("1.1.1.1")
            .addDnsServer("8.8.8.8")
            .addDisallowedApplication(context.packageName)
        if (Build.VERSION.SDK_INT >= 29) builder.setMetered(false)
        val fd = builder.establish() ?: error("vpn permission missing")
        try {
            if (!envReady) {
                Libv2ray.initCoreEnv(context.filesDir.absolutePath, "")
                envReady = true
            }
            val core = Libv2ray.newCoreController(object : CoreCallbackHandler {
                override fun startup(): Long = 0
                override fun shutdown(): Long = 0
                override fun onEmitStatus(p0: Long, p1: String?): Long = 0
            })
            core.startLoop(config, fd.fd)
            check(core.isRunning)
            controller = core
            tun = fd
        } catch (e: Exception) {
            try { fd.close() } catch (_: Exception) {}
            stopService(context)
            throw e
        }
    }

    /** 经出口发一个真实 HTTPS 请求；成功才算「已连通」。返回毫秒，失败返回 null。 */
    fun probe(): Long? {
        val core = controller ?: return null
        for (url in listOf("https://www.gstatic.com/generate_204", "https://cp.cloudflare.com/generate_204")) {
            val ms = try { core.measureDelay(url) } catch (_: Exception) { -1L }
            if (ms >= 0) return ms
        }
        return null
    }

    fun traffic(): Pair<Long, Long> = XrayPolicy.trafficDelta(controller?.queryAllOutboundTrafficStats())

    private fun stopCore() {
        val core = controller
        controller = null
        try { core?.stopLoop() } catch (_: Exception) {}
        // 内核不持有 fd 的所有权（Android TUN 的 Close 为空操作），必须在停内核之后由我们关闭。
        try { tun?.close() } catch (_: Exception) {}
        tun = null
    }

    /** 完整断开：内核、VPN 接口、服务。没有在运行的也可安全调用。 */
    fun stop(context: Context) {
        stopCore()
        stopService(context)
    }

    private fun stopService(context: Context) {
        // 先解除引用：旧实例的异步销毁不会被误当成新连接要用的服务。
        val old = synchronized(lock) { val s = service; service = null; s }
        if (old != null) {
            old.stopSelf()
            context.stopService(Intent(context, XrayVpnService::class.java))
        }
    }
}

/** 只负责向系统申请 VPN 接口与接收撤销通知；内核与状态都在 XrayTunnel。 */
class XrayVpnService : VpnService() {
    override fun onCreate() { super.onCreate(); XrayTunnel.attach(this) }
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        XrayTunnel.attach(this)
        return START_NOT_STICKY
    }
    // 被系统或其他 VPN 应用接管：如实通知上层，不自动重连。
    override fun onRevoke() { XrayTunnel.onRevoked?.invoke(); super.onRevoke() }
    override fun onDestroy() { XrayTunnel.detach(this); super.onDestroy() }
}
