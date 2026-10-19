import java.net.URI
import java.io.File
import java.security.KeyStore
import java.security.cert.X509Certificate
import java.util.Base64
import java.security.MessageDigest

// 多协议候选版：独立包名与名称，和正式版并存安装、互不覆盖。仅由构建脚本设置。
val multiprotoCandidate = System.getenv("YH_CANDIDATE") == "multiproto"
// 模拟器测试包：release 模式但用公开的调试证书签名，不读取正式钥匙。正式构建不要设置。
val debugSignedRelease = System.getenv("YH_RELEASE_DEBUG_SIGN") == "1"
// Xray 内核（AndroidLibXrayLite v26.9.9，LGPL-3.0，预编译 AAR）；构建前核对 SHA-256。
val xrayAar = "libs/libv2ray.aar"
val xrayAarSha256 = "9ecf4c921568d8f4cb8550d3bafe08ff6f1d1984f45a6ad183dcdf52ee9302de"

plugins {
    id("com.android.application")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

android {
    namespace = "com.yuhai.yuhai_vpn"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    defaultConfig {
        // TODO: Specify your own unique Application ID (https://developer.android.com/studio/build/application-id.html).
        applicationId = "com.yuhai.yuhai_vpn"
        // You can update the following values to match your application needs.
        // For more information, see: https://flutter.dev/to/review-gradle-config.
        minSdk = maxOf(flutter.minSdkVersion, 26)
        targetSdk = flutter.targetSdkVersion
        // Uses the version code from pubspec.yaml. When using split APKs, 1000 * ABI_VERSION
        // is added automatically by Flutter. (https://developer.android.com/studio/build/configure-apk-splits#configure-APK-versions)
        // You can force using the value of versionCode by specifying the `-P force-version-code-ignoring-abi=true`
        // flag during build.
        versionCode = flutter.versionCode
        versionName = flutter.versionName
        manifestPlaceholders["appLabel"] = if (multiprotoCandidate) "宇海VPN" else "宇海"
        // 仅 CI 模拟器探测打开。正式构建不设置，活动保持禁用。
        manifestPlaceholders["vpnProbeEnabled"] = if (System.getenv("YH_VPN_PROBE") == "1") "true" else "false"
        if (multiprotoCandidate) applicationIdSuffix = ".candidate"
    }

    // 原生库在安装包里压缩存放：下载体积明显变小（官网服务器带宽有限），安装时由系统解压。
    packaging {
        jniLibs {
            useLegacyPackaging = true
            // Xray / WireGuard 每个架构大约几十 MB。用打包排除而不是 abiFilters：
            // Flutter 插件会自己补 abiFilters。
            // YH_ABI=arm64-v8a | x86_64 | x86 | armeabi-v7a | all。
            // 未设置时，多协议候选版仍只带 arm64-v8a（真机）。模拟器测试包设 YH_ABI=x86_64。
            val requestedAbi = System.getenv("YH_ABI")?.trim().orEmpty()
            val allAbis = listOf("armeabi-v7a", "arm64-v8a", "x86", "x86_64")
            val keep = when {
                requestedAbi == "all" -> allAbis.toSet()
                requestedAbi.isNotEmpty() -> setOf(requestedAbi)
                multiprotoCandidate -> setOf("arm64-v8a")
                else -> allAbis.toSet()
            }
            check(keep.all { it in allAbis }) { "YH_ABI 无效：$requestedAbi" }
            excludes += allAbis.filter { it !in keep }.map { "lib/$it/**" }
        }
    }

    // 内核 AAR 自带的 geoip/geosite 数据约 28MB，本项目配置不引用（分流用网段列表），不打进安装包。
    androidResources {
        ignoreAssetsPatterns += listOf("!geoip.dat", "!geosite.dat", "!geoip-only-cn-private.dat")
    }

    signingConfigs {
        create("production") {
            val location = System.getenv("YH_ANDROID_KEYSTORE")
            if (!location.isNullOrBlank()) storeFile = file(location)
            storePassword = System.getenv("YH_ANDROID_STORE_PASSWORD")
            keyAlias = System.getenv("YH_ANDROID_KEY_ALIAS")
            keyPassword = System.getenv("YH_ANDROID_KEY_PASSWORD")
        }
    }

    buildTypes {
        release {
            signingConfig = if (debugSignedRelease) {
                signingConfigs.getByName("debug").apply {
                    storeFile = file("${System.getProperty("user.home")}/.android/debug.keystore")
                    storePassword = "android"
                    keyAlias = "androiddebugkey"
                    keyPassword = "android"
                }
            } else {
                signingConfigs.getByName("production")
            }
            isDebuggable = false
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

flutter {
    source = "../.."
}

dependencies {
    implementation("com.wireguard.android:tunnel:1.0.20260102")
    implementation(files(xrayAar))
    testImplementation("junit:junit:4.13.2")
    // 本地单元测试需要真实 org.json（安卓 SDK 里的只是占位实现）。
    testImplementation("org.json:json:20250517")
}

// Fail before packaging: runtime checks alone can still produce an unusable or
// accidentally unsigned release. Secrets come only from the build environment.
val verifyReleaseSecurity by tasks.registering {
    doLast {
        val target = project.findProperty("target")?.toString() ?: "lib/main.dart"
        val sourceRoot = rootProject.projectDir.parentFile
        val targetFile = if (File(target).isAbsolute) File(target) else sourceRoot.resolve(target)
        check(targetFile.canonicalFile == sourceRoot.resolve("lib/main.dart").canonicalFile) {
            "Release builds must use the production lib/main.dart entry point"
        }
        val defines = (project.findProperty("dart-defines")?.toString() ?: "")
            .split(',').filter { it.isNotEmpty() }.map {
                String(Base64.getDecoder().decode(it), Charsets.UTF_8)
            }
        val api = defines.singleOrNull { it.startsWith("YH_API_BASE=") }
            ?.substringAfter('=') ?: error("Release requires an explicit HTTPS YH_API_BASE")
        val uri = runCatching { URI(api) }.getOrNull()
        val host = uri?.host?.lowercase() ?: ""
        check(uri != null && uri.scheme == "https" && host.contains('.') &&
            host.split('.').all { it.matches(Regex("[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?")) } &&
            !host.matches(Regex("[0-9.]+")) &&
            listOf(".localhost", ".local", ".test", ".invalid").none(host::endsWith) &&
            uri.rawUserInfo == null && uri.rawQuery == null && uri.rawFragment == null &&
            (uri.path.isNullOrEmpty() || uri.path == "/") &&
            (uri.port == -1 || uri.port in 1..65535)) {
            "Release API must be an HTTPS domain without credentials, query, or fragment"
        }
        if (debugSignedRelease) {
            val signing = android.signingConfigs.getByName("debug")
            check(signing.storeFile?.isFile == true && signing.keyAlias == "androiddebugkey") {
                "Debug-signed release requires the Android debug keystore"
            }
            return@doLast
        }
        val signing = android.signingConfigs.getByName("production")
        check(signing.storeFile?.isFile == true &&
            signing.storeFile?.name != "debug.keystore" &&
            !signing.storePassword.isNullOrBlank() &&
            !signing.keyPassword.isNullOrBlank() &&
            !signing.keyAlias.isNullOrBlank() && signing.keyAlias != "androiddebugkey") {
            "Release requires a production keystore and signing environment"
        }
        // Inspect the certificate rather than trusting a renamed debug.keystore.
        val password = signing.storePassword!!.toCharArray()
        try {
            val keyStore = KeyStore.getInstance(signing.storeFile!!, password)
            val certificate = keyStore.getCertificate(signing.keyAlias) as? X509Certificate
            check(certificate != null &&
                !certificate.subjectX500Principal.name.contains("CN=Android Debug", ignoreCase = true)) {
                "A debug signing certificate cannot be used for release"
            }
            certificate.checkValidity()
        } finally { password.fill('\u0000') }
    }
}
val verifyXrayCore by tasks.registering {
    doLast {
        val aar = file(xrayAar)
        check(aar.isFile) { "缺少 Xray 内核 $xrayAar：请用 deploy/build-android-candidate.sh 构建（会自动下载并核对）" }
        val digest = MessageDigest.getInstance("SHA-256").digest(aar.readBytes()).joinToString("") { "%02x".format(it) }
        check(digest == xrayAarSha256) { "Xray 内核 AAR 的 SHA-256 不符：$digest" }
    }
}
tasks.configureEach {
    if (name == "preReleaseBuild") dependsOn(verifyReleaseSecurity)
    if (name == "preBuild") dependsOn(verifyXrayCore)
}

