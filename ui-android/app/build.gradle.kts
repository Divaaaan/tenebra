plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
}

android {
    namespace = "com.tenebra.android"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.tenebra.android"
        minSdk = 23
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"

        // Only the ABIs sing-box ships prebuilt for; drop x86 (no phones, larger APK).
        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a", "x86_64")
        }
    }

    buildTypes {
        release {
            // Kept off until the libbox/gomobile keep-rules are proven on CI. R8 will
            // otherwise strip the JNI-reflected Seq binding classes and the engine
            // fails to load. Turn on together with a verified proguard-rules.pro.
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    packaging {
        // The gomobile .aar bundle their own copies of these; drop the duplicates so
        // packaging does not fail on a merge conflict.
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }
}

dependencies {
    // The two gomobile artifacts. Both are git-ignored and dropped in by the build
    // (see README "Bring-up order"): libbox.aar is the unmodified sing-box engine,
    // tenebra-core.aar is our config generator (core-bridge). files() is deliberate —
    // a gomobile .aar bundles the Go runtime statically and has no transitive Maven
    // deps, and a missing file must fail the build loudly rather than resolve a
    // stand-in. Order matches the paths the CI bind command writes to.
    implementation(files("libs/libbox.aar", "libs/tenebra-core.aar"))

    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.lifecycle.service)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.activity.compose)

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    debugImplementation(libs.androidx.compose.ui.tooling)

    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.kotlinx.serialization.json)
}
