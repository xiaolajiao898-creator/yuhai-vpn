package com.yuhai.yuhai_vpn

import org.junit.Assert.*
import org.junit.Test
import java.time.Instant
import java.util.Base64

class WireGuardPolicyTest {
    private val now = Instant.parse("2026-09-18T01:00:00Z").toEpochMilli()
    private fun profile() = mutableMapOf<String, Any>(
        "identity" to "123:456", "generation" to 1, "nodeId" to "1", "protocol" to "WIREGUARD",
        "expiresAt" to "2026-09-18T01:05:00.000Z", "endpoint" to "vpn.example.com:51820",
        "serverPublicKey" to Base64.getEncoder().encodeToString(ByteArray(32) { 2 }),
        "address" to "10.20.0.16/32", "dns" to listOf("1.1.1.1", "2606:4700:4700::1111"), "mtu" to 1280)
    @Test fun validPublicProfileAndIPv6Endpoint() {
        assertEquals("123:456", WireGuardPolicy.parse(profile(), now).identity)
        assertTrue(WireGuardPolicy.endpoint("[2606:4700:4700::1111]:51820"))
        assertFalse(WireGuardPolicy.endpoint("[1.1.1.1]:51820"))
    }
    @Test fun rejectsMalformedOrInjectedMaterialBeforeTunnelMutation() {
        val invalid = listOf<Pair<String, Any>>(
            "identity" to "123:456\n", "generation" to 1.5, "generation" to 0,
            "nodeId" to "01", "protocol" to "MOCK", "privateKey" to "never-accepted",
            "expiresAt" to "2026-09-18T01:00:00Z", "expiresAt" to "2026-09-18T01:10:00Z",
            "expiresAt" to "2026-09-31T01:01:00Z", "expiresAt" to "2026-09-18T01:01:00+00:00",
            "address" to "8.8.8.8/32", "address" to "10.999.0.1/32", "address" to "10.1.1.1/24",
            "address" to "010.1.1.1/32", "endpoint" to "host:0", "endpoint" to "vpn.example.com:65536",
            "endpoint" to "user@vpn.example.com:51820", "endpoint" to "vpn.example.com:51820\nPrivateKey=x",
            "endpoint" to "999.1.1.1:51820", "endpoint" to "[:::]:51820",
            "serverPublicKey" to Base64.getEncoder().encodeToString(ByteArray(32)),
            "serverPublicKey" to "bad", "dns" to emptyList<String>(),
            "dns" to listOf("1.1.1.1", "1.1.1.1"), "dns" to listOf("example.com"),
            "dns" to listOf("::"), "dns" to listOf("1::2::3"), "dns" to listOf("ff02::1"),
            "mtu" to 1279, "mtu" to 1501, "mtu" to 1280.5)
        for ((field, value) in invalid) {
            val p = profile(); p[field] = value
            try { WireGuardPolicy.parse(p, now); fail("accepted $field=$value") }
            catch (_: Exception) {}
        }
    }
}
