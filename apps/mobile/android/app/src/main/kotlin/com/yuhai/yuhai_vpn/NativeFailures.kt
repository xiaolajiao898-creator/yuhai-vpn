package com.yuhai.yuhai_vpn

/**
 * Native VPN libraries throw [Error] (for example [UnsatisfiedLinkError] under
 * ARM translation) rather than [Exception]. Those must become a channel error.
 * [VirtualMachineError] is still fatal: swallowing out-of-memory hides a real crash.
 */
internal object NativeFailures {
    fun rethrowIfFatal(error: Throwable) {
        if (error is VirtualMachineError) throw error
    }

    fun code(error: Throwable): String = when (error) {
        is LinkageError -> "ENGINE_NATIVE"
        else -> "CONNECT_FAILED"
    }
}
