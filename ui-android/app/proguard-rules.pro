# R8/ProGuard rules for the release build.
#
# Minification is currently disabled in build.gradle.kts (isMinifyEnabled = false)
# because the gomobile keep-rules below are UNVERIFIED against the real libbox.aar —
# gomobile binds Java classes that Go calls back by JNI reflection, and R8 will strip
# them without an explicit keep, crashing the engine at runtime. Prove these on CI
# with a shrunk build before flipping minification on.

# gomobile / go-mobile seq bindings (the Go<->Java bridge classes).
-keep class go.** { *; }
-keep class io.nekohasekai.libbox.** { *; }
-keep class com.tenebra.core.** { *; }

# Our platform-interface implementation is instantiated and called from Go by name;
# keep it and its members so reflection from libbox resolves.
-keep class com.tenebra.android.bg.** { *; }

# kotlinx.serialization keeps its generated serializers via annotations; the plugin
# adds most rules, but keep the @Serializable metadata to be safe.
-keepattributes *Annotation*, InnerClasses
-keepclassmembers class **$$serializer { *; }
