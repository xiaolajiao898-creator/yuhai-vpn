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
 * 新接口已经建立之后，才允许关掉旧接口。保护开启时，失败要留下黑洞接口，不能先拆掉再申请。
 */
internal enum class InterfaceStep { ReplaceOld, KeepOld, FailClosed, Release }

internal fun interfaceStep(protect: Boolean, newReady: Boolean, oldOpen: Boolean): InterfaceStep = when {
    newReady -> InterfaceStep.ReplaceOld
    protect && oldOpen -> InterfaceStep.KeepOld
    protect -> InterfaceStep.FailClosed
    else -> InterfaceStep.Release
}

/**
 * Xray 内核 + 系统 VPN 接口的生命周期。所有方法都只在 WireGuardChannel 的单线程执行器上调用，
 * 与 WireGuard 隧道的增删串行，天然不会同时存在两条隧道。
 *
 * 路由：IPv4 与 IPv6 全部进入 TUN（与 WireGuard 路径一致，IPv6 不能绕过）；本应用自身排除在外，
 * 内核连出口服务器的流量因此走物理网络而不会回环。DNS 指向 1.1.1.1 / 8.8.8.8，
 * 进入 TUN 后被内核规则截获，经出口以 DoH 解析，不在本地网络明文泄露。
 *
 * 断网保护：重连时停内核但留下不读取的 TUN，系统继续把流量送进这个黑洞。新的 establish
 * 成功之后才关闭旧 fd。只有用户断开才 stopService。进程被杀死后这个接口会消失，
 * 所以还要引导用户打开系统的「始终开启 VPN」和「无 VPN 时阻止连接」。
 */
internal object XrayTunnel {
    private val lock = Object()
    private var service: XrayVpnService? = null
    @Volatile private var controller: CoreController? = null
    private var tun: ParcelFileDescriptor? = null
    private var envReady = false
    @Volatile var onRevoked: (() -> Unit)? = null
    @Volatile var leakProtect: Boolean = true

    val running: Boolean get() = controller?.isRunning == true
    val interfaceOpen: Boolean get() = tun != null

    fun attach(s: XrayVpnService) = synchronized(lock) { service = s; lock.notifyAll() }
    fun detach(s: XrayVpnService) = synchronized(lock) { if (service === s) service = null }
    fun serviceOrNull(): XrayVpnService? = synchronized(lock) { service }

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
        // 先停内核，旧接口先留着。openInterface 只有在新 fd 建立成功后才关掉旧的。
        stopCoreOnly()
        val fd = openInterface(context)
        if (fd == null) {
            if (interfaceStep(leakProtect, newReady = false, oldOpen = tun != null) == InterfaceStep.Release) {
                stop(context)
            }
            error("vpn permission missing")
        }
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
        } catch (e: Throwable) {
            NativeFailures.rethrowIfFatal(e)
            controller = null
            if (leakProtect) {
                // 不读这个 fd，流量继续掉进黑洞。不要先关掉再重建。
                if (tun !== fd) tun = fd
            } else {
                try { fd.close() } catch (closeError: Throwable) { NativeFailures.rethrowIfFatal(closeError) }
                if (tun === fd) tun = null
                stopService(context)
            }
            throw e
        }
    }

    /** 停内核，留下不读取的 TUN，让系统流量继续掉进黑洞。返回 false 表示接口没留住。 */
    fun hold(context: Context): Boolean {
        stopCoreOnly()
        if (tun != null) return true
        return openInterface(context) != null
    }

    /** 经出口发一个真实 HTTPS 请求；成功才算「已连通」。返回毫秒，失败返回 null。 */
    fun probe(): Long? {
        val core = controller ?: return null
        for (url in listOf("https://www.gstatic.com/generate_204", "https://cp.cloudflare.com/generate_204")) {
            val ms = try { core.measureDelay(url) } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t); -1L }
            if (ms >= 0) return ms
        }
        return null
    }

    fun traffic(): Pair<Long, Long> = XrayPolicy.trafficDelta(controller?.queryAllOutboundTrafficStats())

    private fun openInterface(context: Context): ParcelFileDescriptor? {
        if (synchronized(lock) { service } == null) {
            context.startService(Intent(context, XrayVpnService::class.java))
        }
        val svc = awaitService(5000) ?: return null
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
        val fd = builder.establish() ?: return null
        val previous = tun
        tun = fd
        if (previous != null && previous !== fd) {
            try { previous.close() } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) }
        }
        return fd
    }

    private fun stopCoreOnly() {
        val core = controller
        controller = null
        try { core?.stopLoop() } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) }
    }

    private fun stopCore() {
        stopCoreOnly()
        // 内核不持有 fd 的所有权（Android TUN 的 Close 为空操作），必须在停内核之后由我们关闭。
        try { tun?.close() } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) }
        tun = null
    }

    /** 完整断开：内核、VPN 接口、服务。没有在运行的也可安全调用。只有用户断开才走这里。 */
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
