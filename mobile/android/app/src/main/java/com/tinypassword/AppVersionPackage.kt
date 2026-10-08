package com.tinypassword

import com.facebook.react.ReactPackage
import com.facebook.react.bridge.NativeModule
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.uimanager.ViewManager

/** Read the version of the installed APK, never the JS package version. */
class AppVersionModule(context: ReactApplicationContext) : ReactContextBaseJavaModule(context) {
  override fun getName() = "AppVersion"

  override fun getConstants(): Map<String, Any> = mapOf(
    "versionName" to BuildConfig.VERSION_NAME,
    "versionCode" to BuildConfig.VERSION_CODE,
  )
}

class AppVersionPackage : ReactPackage {
  override fun createNativeModules(context: ReactApplicationContext): List<NativeModule> =
    listOf(AppVersionModule(context))

  override fun createViewManagers(context: ReactApplicationContext): List<ViewManager<*, *>> =
    emptyList()
}
