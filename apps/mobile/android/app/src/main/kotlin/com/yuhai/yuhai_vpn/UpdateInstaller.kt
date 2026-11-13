package com.yuhai.yuhai_vpn

import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.FileProvider
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import java.io.File
import java.security.MessageDigest

/// 校验安装包签名和已安装应用一致后，交给系统安装器。校验失败就删掉文件。
class UpdateInstaller(private val activity: MainActivity) : MethodChannel.MethodCallHandler {
    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        if (call.method == "updateDir") {
            val dir = File(activity.cacheDir, "updates")
            dir.mkdirs()
            result.success(dir.absolutePath)
            return
        }
        if (call.method != "installApk") {
            result.notImplemented()
            return
        }
        val path = call.argument<String>("path")
        if (path.isNullOrBlank()) {
            result.error("BAD_ARGS", "missing path", null)
            return
        }
        val file = File(path)
        try {
            val allowed = File(activity.cacheDir, "updates").canonicalPath
            if (!file.isFile || !file.canonicalPath.startsWith(allowed + File.separator)) {
                delete(file)
                result.error("BAD_PATH", "update file is outside the update directory", null)
                return
            }
            if (!sameCertificate(file)) {
                delete(file)
                result.error("CERT_MISMATCH", "apk certificate does not match the installed app", null)
                return
            }
            val uri = FileProvider.getUriForFile(activity, "${activity.packageName}.update", file)
            val intent = Intent(Intent.ACTION_VIEW).apply {
                setDataAndType(uri, "application/vnd.android.package-archive")
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            }
            activity.startActivity(intent)
            result.success(null)
        } catch (t: Throwable) {
            delete(file)
            result.error("INSTALL_FAILED", "could not open the installer", null)
        }
    }

    private fun sameCertificate(apk: File): Boolean {
        val flags = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
        val installed = activity.packageManager.getPackageInfo(activity.packageName, flags)
        val incoming = activity.packageManager.getPackageArchiveInfo(apk.absolutePath, flags) ?: return false
        val left = fingerprints(installed)
        val right = fingerprints(incoming)
        return left.isNotEmpty() && left == right
    }

    private fun fingerprints(info: android.content.pm.PackageInfo): Set<String> {
        val signatures = if (Build.VERSION.SDK_INT >= 28) {
            info.signingInfo?.apkContentsSigners ?: emptyArray()
        } else {
            @Suppress("DEPRECATION")
            info.signatures ?: emptyArray()
        }
        return signatures.map { cert ->
            MessageDigest.getInstance("SHA-256").digest(cert.toByteArray()).joinToString("") { "%02x".format(it) }
        }.toSet()
    }

    private fun delete(file: File) {
        if (file.exists()) file.delete()
    }
}
