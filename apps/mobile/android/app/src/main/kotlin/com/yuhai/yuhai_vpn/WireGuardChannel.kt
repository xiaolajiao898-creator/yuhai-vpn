package com.yuhai.yuhai_vpn

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import com.wireguard.android.backend.GoBackend
import com.wireguard.android.backend.Tunnel
import com.wireguard.config.Config
import com.wireguard.config.Interface
import com.wireguard.config.Peer
import com.wireguard.crypto.Key
import com.wireguard.crypto.KeyPair
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import java.net.InetSocketAddress
import java.net.Socket
import java.security.KeyStore
import java.security.MessageDigest
import java.time.Instant
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicLong
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Native WireGuard private keys never leave this class or appear in events.
 * 同一通道也承载出口（Xray 内核）连接：两种隧道共用同一个代次计数与单线程执行器，
 * 任何一次连接/断开都会先把另一种停掉，因此不会同时存在两条隧道。
 */
class WireGuardChannel(private val activity: Activity) : MethodChannel.MethodCallHandler, EventChannel.StreamHandler {
    companion object { const val PERMISSION_REQUEST = 23071 }
    private val main = Handler(Looper.getMainLooper())
    private val executor = Executors.newSingleThreadExecutor()
    private val probes = Executors.newFixedThreadPool(4)
    private val generation = AtomicLong(0)
    private val backend by lazy { GoBackend(activity.applicationContext) }
    @Volatile private var sink: EventChannel.EventSink? = null
    private var permission: MethodChannel.Result? = null
    private var permissionInFlight = false
    @Volatile private var identity: String? = null
    private var tunnel: Tunnel? = null
    private var peerKey: Key? = null
    private var startedAt = 0L
    private var startedMono = 0L
    private var lastMono = 0L
    private var lastRx = 0L
    private var lastTx = 0L
    private var connected = false
    @Volatile private var disposed = false
    private var xrayUp = false
    private var xraySamples = 0

    init {
        // 被系统或其他 VPN 应用接管：如实报告，不自动抢回。
        XrayTunnel.onRevoked = {
            if (!disposed) try {
                executor.execute {
                    val gen = generation.get(); val id = identity
                    if (xrayUp && id != null) { try { stop() } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) }; event(gen, id, mapOf("type" to "error", "code" to "VPN_REVOKED")) }
                }
            } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) } // 执行器已关闭（页面已销毁）：隧道已随之停止
        }
    }

    override fun onListen(arguments: Any?, events: EventChannel.EventSink) { sink = events }
    override fun onCancel(arguments: Any?) { sink = null }
    private fun success(result: MethodChannel.Result, value: Any?) = main.post { result.success(value) }
    private fun failure(result: MethodChannel.Result, code: String) = main.post { result.error(code, "VPN operation failed", null) }
    private fun event(gen: Long, id: String, values: Map<String, Any>) = main.post {
        if (!disposed && gen == generation.get() && id == identity) sink?.success(values + mapOf("generation" to gen, "identity" to id))
    }
    private fun validIdentity(id: String) = WireGuardPolicy.identity(id)

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        if (disposed) { failure(result,"ENGINE_UNAVAILABLE"); return }
        when (call.method) {
            "prepare" -> {
                if (permissionInFlight) { failure(result,"PERMISSION_DENIED"); return }
                val intent = VpnService.prepare(activity)
                if (intent == null) result.success(true) else {
                    permission = result; permissionInFlight = true
                    try { activity.startActivityForResult(intent, PERMISSION_REQUEST) }
                    catch (t: Throwable) { NativeFailures.rethrowIfFatal(t); permission = null; permissionInFlight = false; failure(result,"PERMISSION_DENIED") }
                }
            }
            "publicKey" -> {
                val id = call.argument<String>("identity") ?: ""
                if (!validIdentity(id)) { failure(result,"CONNECT_FAILED"); return }
                executor.execute { try { success(result,keyPair(id).publicKey.toBase64()) } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t); failure(result, NativeFailures.code(t)) } }
            }
            "connect" -> {
                val gen = WireGuardPolicy.integer(call.argument<Any>("generation")) ?: -1
                if (gen <= generation.get()) { failure(result,"CONNECT_FAILED"); return }
                generation.set(gen)
                executor.execute { connect(call, result, gen) }
            }
            "connectXray" -> {
                val gen = WireGuardPolicy.integer(call.argument<Any>("generation")) ?: -1
                if (gen <= generation.get()) { failure(result,"CONNECT_FAILED"); return }
                generation.set(gen)
                executor.execute { connectXray(call, result, gen) }
            }
            "disconnect" -> {
                val gen = WireGuardPolicy.integer(call.argument<Any>("generation")) ?: -1
                if (gen <= generation.get()) { failure(result,"DISCONNECT_FAILED"); return }
                generation.set(gen) // invalidate an in-flight connection immediately
                permission?.success(false); permission = null
                executor.execute {
                    try { stop(); identity = null; success(result,null) } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t); failure(result,"DISCONNECT_FAILED") }
                }
            }
            "probe" -> {
                val host=call.argument<String>("host") ?: ""
                val port=call.argument<Number>("port")?.toInt() ?: 0
                val timeout=call.argument<Number>("timeoutMs")?.toInt() ?: 2000
                if (!WireGuardPolicy.host(host) || port !in 1..65535 || timeout !in 1..5000) { result.success(null); return }
                probes.execute { val start=SystemClock.elapsedRealtime(); try { Socket().use { it.connect(InetSocketAddress(host,port),timeout) };success(result,SystemClock.elapsedRealtime()-start) } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t); success(result,null) } }
            }
            else -> result.notImplemented()
        }
    }
    fun onActivityResult(requestCode: Int, resultCode: Int): Boolean {
        if (requestCode != PERMISSION_REQUEST) return false
        permission?.success(resultCode == Activity.RESULT_OK); permission = null; permissionInFlight = false; return true
    }
    private fun stop() {
        try { tunnel?.let { backend.setState(it,Tunnel.State.DOWN,null) } }
        finally {
            tunnel=null;peerKey=null;connected=false
            if (xrayUp) { xrayUp=false; XrayTunnel.stop(activity.applicationContext) }
        }
    }
    private fun connectXray(call: MethodCall, result: MethodChannel.Result, gen: Long) {
        try {
            if (gen != generation.get()) { failure(result,"CONNECT_FAILED"); return }
            val profile = try { XrayPolicy.parse(call.arguments as? Map<*, *> ?: emptyMap<Any,Any>(), System.currentTimeMillis()) }
                catch (t: Throwable) { NativeFailures.rethrowIfFatal(t); failure(result,"PROFILE_INVALID"); return }
            if (VpnService.prepare(activity) != null) { failure(result,"PERMISSION_DENIED"); return }
            stop()
            if (gen != generation.get()) { failure(result,"CONNECT_FAILED"); return }
            val id = profile.identity
            identity=id;startedAt=System.currentTimeMillis();startedMono=SystemClock.elapsedRealtime();lastMono=startedMono;lastRx=0;lastTx=0;xraySamples=0
            event(gen,id,mapOf("type" to "status","status" to "connecting"))
            xrayUp = true
            XrayTunnel.start(activity.applicationContext, profile.config)
            if (gen != generation.get()) { stop();failure(result,"CONNECT_FAILED");return }
            success(result,null)
            confirmXray(gen,id)
            main.postDelayed({ sampleXray(gen,id) },1000)
        } catch (t: Throwable) {
            NativeFailures.rethrowIfFatal(t)
            try { stop() } catch (stopError: Throwable) { NativeFailures.rethrowIfFatal(stopError) }
            failure(result, NativeFailures.code(t))
        }
    }
    /** 「已连通」只在经出口的真实 HTTPS 请求成功后才上报；25 秒内不成功即断开并报超时。 */
    private fun confirmXray(gen: Long, id: String) = try { probes.execute {
            val deadline = SystemClock.elapsedRealtime() + 25000
            var ok = false
            while (!disposed && gen == generation.get() && SystemClock.elapsedRealtime() < deadline) {
                if (XrayTunnel.probe() != null) { ok = true; break }
                SystemClock.sleep(1500)
            }
            later {
                if (disposed || gen != generation.get() || !xrayUp) return@later
                if (ok) {
                    val now = System.currentTimeMillis()
                    connected = true; startedMono = SystemClock.elapsedRealtime()
                    event(gen,id,mapOf("type" to "status","status" to "connected","handshakeConfirmed" to true,"handshakeAt" to Instant.ofEpochMilli(now).toString()))
                } else {
                    try { stop() } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) }
                    event(gen,id,mapOf("type" to "error","code" to "HANDSHAKE_TIMEOUT"))
                }
            }
        }
    } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) }
    /** 从探测线程回到执行器；页面已销毁、执行器已关闭时静默放弃（隧道已随销毁停止）。 */
    private fun later(block: () -> Unit) { try { executor.execute(block) } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) } }
    private fun sampleXray(gen: Long, id: String) {
        if (disposed || gen != generation.get()) return
        executor.execute {
            try {
                if (gen != generation.get() || !xrayUp) return@execute
                if (!XrayTunnel.running) {
                    try { stop() } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) }
                    event(gen,id,mapOf("type" to "error","code" to "NETWORK_UNAVAILABLE")); return@execute
                }
                val (up, down) = XrayTunnel.traffic()
                lastTx += up; lastRx += down
                if (connected) {
                    val mono = SystemClock.elapsedRealtime(); val delta = (mono - lastMono).coerceAtLeast(1)
                    event(gen,id,mapOf("type" to "stats","upBytes" to lastTx,"downBytes" to lastRx,"upSpeed" to up*1000/delta,"downSpeed" to down*1000/delta,"since" to startedAt))
                    lastMono = mono
                    // 每分钟经出口复核一次；连不上就如实进入「重连中」，恢复后重新确认。
                    if (++xraySamples % 60 == 0) try { probes.execute {
                        if (XrayTunnel.probe() == null) later {
                            if (gen == generation.get() && xrayUp && connected) {
                                connected = false; startedAt = System.currentTimeMillis()
                                event(gen,id,mapOf("type" to "status","status" to "reconnecting"))
                                confirmXray(gen,id)
                            }
                        }
                    } } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) }
                } else lastMono = SystemClock.elapsedRealtime()
                main.postDelayed({ sampleXray(gen,id) },1000)
            } catch (t: Throwable) {
                NativeFailures.rethrowIfFatal(t)
                try { stop() } catch (stopError: Throwable) { NativeFailures.rethrowIfFatal(stopError) }
                val code = if (NativeFailures.code(t) == "ENGINE_NATIVE") "ENGINE_NATIVE" else "NETWORK_UNAVAILABLE"
                event(gen,id,mapOf("type" to "error","code" to code))
            }
        }
    }
    private fun connect(call: MethodCall, result: MethodChannel.Result, gen: Long) {
        var owned: Tunnel? = null
        try {
            if (gen != generation.get()) { failure(result,"CONNECT_FAILED"); return }
            val profile = WireGuardPolicy.parse(call.arguments as? Map<*, *> ?: emptyMap<Any,Any>(), System.currentTimeMillis())
            val id = profile.identity
            val expires = profile.expires
            if(VpnService.prepare(activity)!=null) { failure(result,"PERMISSION_DENIED"); return }
            val key = Key.fromBase64(profile.publicKey)
            val pair = keyPair(id)
            require(key != pair.publicKey)
            val address = profile.address; val endpoint = profile.endpoint
            val dns = profile.dns; val mtu = profile.mtu
            val iface=Interface.Builder().setKeyPair(pair).parseAddresses(address).parseDnsServers(dns.joinToString(",")).setMtu(mtu).build()
            // Route both families into the tunnel: IPv6 cannot silently bypass it.
            val peer=Peer.Builder().setPublicKey(key).parseEndpoint(endpoint).parseAllowedIPs("0.0.0.0/0, ::/0").setPersistentKeepalive(25).build()
            val config=Config.Builder().setInterface(iface).addPeer(peer).build()
            stop()
            if(gen != generation.get() || expires<=System.currentTimeMillis()) { failure(result,"PROFILE_EXPIRED"); return }
            identity=id;startedAt=System.currentTimeMillis();startedMono=SystemClock.elapsedRealtime();lastMono=startedMono;lastRx=0;lastTx=0
            val current=object: Tunnel {
                override fun getName()="yuhai"
                // Teardown is acknowledged by disconnect only after setState returns.
                // Asynchronous old callbacks must not masquerade as current events.
                override fun onStateChange(state: Tunnel.State) {}
            }
            owned=current;tunnel=current;peerKey=key
            event(gen,id,mapOf("type" to "status","status" to "connecting"))
            backend.setState(current,Tunnel.State.UP,config)
            if(gen != generation.get()) { stop();failure(result,"CONNECT_FAILED");return }
            success(result,null)
            main.postDelayed({ sample(gen,id,current) },1000)
        } catch (t: Throwable) {
            NativeFailures.rethrowIfFatal(t)
            if(owned!=null && tunnel===owned) { try { stop() } catch (stopError: Throwable) { NativeFailures.rethrowIfFatal(stopError) } }
            failure(result, NativeFailures.code(t))
        }
    }
    private fun sample(gen: Long,id: String,current: Tunnel) {
        if(disposed || gen!=generation.get())return
        executor.execute {
            try {
                if(gen!=generation.get() || tunnel!==current)return@execute
                check(backend.getState(current) == Tunnel.State.UP)
                val stats=backend.getStatistics(current)
                val handshake=peerKey?.let { stats.peer(it)?.latestHandshakeEpochMillis() } ?: 0L
                val now=System.currentTimeMillis();val mono=SystemClock.elapsedRealtime()
                if(handshake>=startedAt && handshake<=now+5000) {
                    if(!connected) { connected=true;event(gen,id,mapOf("type" to "status","status" to "connected","handshakeConfirmed" to true,"handshakeAt" to Instant.ofEpochMilli(handshake).toString())) }
                    val rx=stats.totalRx();val tx=stats.totalTx();val delta=(mono-lastMono).coerceAtLeast(1)
                    event(gen,id,mapOf("type" to "stats","upBytes" to tx,"downBytes" to rx,"upSpeed" to ((tx-lastTx).coerceAtLeast(0)*1000/delta),"downSpeed" to ((rx-lastRx).coerceAtLeast(0)*1000/delta),"since" to startedAt))
                    lastRx=rx;lastTx=tx;lastMono=mono
                }
                if(!connected && mono-startedMono>30000) { stop();event(gen,id,mapOf("type" to "error","code" to "HANDSHAKE_TIMEOUT"));return@execute }
                if(connected && handshake>0 && now-handshake>180000) { connected=false;startedMono=mono;startedAt=now;event(gen,id,mapOf("type" to "status","status" to "reconnecting")) }
            } catch (t: Throwable) {
                NativeFailures.rethrowIfFatal(t)
                try { stop() } catch (stopError: Throwable) { NativeFailures.rethrowIfFatal(stopError) }
                val code = if (NativeFailures.code(t) == "ENGINE_NATIVE") "ENGINE_NATIVE" else "NETWORK_UNAVAILABLE"
                event(gen,id,mapOf("type" to "error","code" to code));return@execute
            }
            main.postDelayed({ sample(gen,id,current) },1000)
        }
    }
    private fun keyPair(id: String): KeyPair {
        val hash=MessageDigest.getInstance("SHA-256").digest(id.toByteArray()).joinToString("") { "%02x".format(it) }
        val alias="yuhai.wg.$hash";val prefs=activity.getSharedPreferences("yuhai.wg",0)
        val ks=KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if(!ks.containsAlias(alias)) {
            check(!prefs.contains(hash)) // missing wrapping key must not silently rotate a live peer
            KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES,"AndroidKeyStore").apply {
                init(KeyGenParameterSpec.Builder(alias,KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT).setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build());generateKey()
            }
        }
        val wrapping=ks.getKey(alias,null) as SecretKey
        check(wrapping.encoded == null) // Android Keystore wrapping material is non-exportable
        val existing=prefs.getString(hash,null)
        if(existing!=null) {
            val bytes=Base64.decode(existing,Base64.NO_WRAP);require(bytes.size>28)
            val cipher=Cipher.getInstance("AES/GCM/NoPadding");cipher.init(Cipher.DECRYPT_MODE,wrapping,GCMParameterSpec(128,bytes.copyOfRange(0,12)));cipher.updateAAD(id.toByteArray())
            val plain=cipher.doFinal(bytes.copyOfRange(12,bytes.size))
            try { return KeyPair(Key.fromBytes(plain)) } finally { plain.fill(0) }
        }
        val pair=KeyPair();val plain=pair.privateKey.bytes
        try {
            val cipher=Cipher.getInstance("AES/GCM/NoPadding");cipher.init(Cipher.ENCRYPT_MODE,wrapping);cipher.updateAAD(id.toByteArray());val sealed=cipher.iv+cipher.doFinal(plain)
            check(prefs.edit().putString(hash,Base64.encodeToString(sealed,Base64.NO_WRAP)).commit())
        } finally { plain.fill(0) }
        return pair
    }
    fun dispose() {
        disposed=true;generation.incrementAndGet();permission?.success(false);permission=null;sink=null
        XrayTunnel.onRevoked = null
        executor.execute { try { stop() } catch (t: Throwable) { NativeFailures.rethrowIfFatal(t) } };executor.shutdown();probes.shutdownNow()
    }
}
