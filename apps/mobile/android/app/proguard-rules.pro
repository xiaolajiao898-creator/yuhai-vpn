# Flutter 的 release 默认打开 R8（压缩、优化、混淆）。gomobile 和 WireGuard
# 从 JNI 按类名、方法名查找；这些类被改名或删掉时，内核加载会直接崩溃。
# 应用自身的 Kotlin 仍允许压缩和混淆。

-keep class go.** { *; }
-keep class libv2ray.** { *; }
-keep class * implements libv2ray.CoreCallbackHandler { *; }
-keep class * implements libv2ray.ProcessFinder { *; }

-keep class com.wireguard.** { *; }

-keepclasseswithmembernames class * {
    native <methods>;
}

# mobile_scanner 自带的规则只保留 com.google.mlkit.*，不含子包。
# R8 会删掉这些 Registrar 的无参构造，启动时反射就报 NoSuchMethodException。
-keep class com.google.mlkit.** { *; }
-dontwarn com.google.mlkit.**
