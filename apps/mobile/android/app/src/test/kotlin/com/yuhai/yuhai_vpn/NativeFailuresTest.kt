package com.yuhai.yuhai_vpn

import org.junit.Assert.assertEquals
import org.junit.Test

class NativeFailuresTest {
    @Test fun linkageErrorsAreEngineNative() {
        assertEquals("ENGINE_NATIVE", NativeFailures.code(UnsatisfiedLinkError("libgojni.so")))
        assertEquals(
            "ENGINE_NATIVE",
            NativeFailures.code(ExceptionInInitializerError(UnsatisfiedLinkError("libgojni.so")))
        )
    }

    @Test fun ordinaryFailuresStayConnectFailed() {
        assertEquals("CONNECT_FAILED", NativeFailures.code(IllegalStateException("tunnel")))
    }

    @Test fun outOfMemoryStaysFatal() {
        try {
            NativeFailures.rethrowIfFatal(OutOfMemoryError("oom"))
            throw AssertionError("OutOfMemoryError was swallowed")
        } catch (error: OutOfMemoryError) {
            assertEquals("oom", error.message)
        }
    }
}
