# RAR Dual-Band ATAK plugin

ATAK-CIV 5.8 plugin that copies this device's **position reports** and the
**GeoChat messages it sends** to the radio over UDP:

| Plugin setting                | Destination port | What the radio does with the copy                         |
|-------------------------------|------------------|-----------------------------------------------------------|
| Automatic (default)           | 6700             | Sends it over Meshtastic only when HaLow has no neighbors |
| Always send over Meshtastic   | 6701             | Always sends it over Meshtastic                           |

ATAK keeps sending everything normally over HaLow; the plugin only adds the
copy. The plugin never receives anything — traffic that arrives over
Meshtastic is delivered to ATAK by the radio itself (UDP to ATAK's input on
port 4242).

## How the radio is found

No IP address is configured. The phone is on the radio's Wi-Fi access point
and each openMANET node runs its own DHCP server, so the plugin sends to the
**DHCP server that gave the phone its address** (Android 11+), falling back
to the legacy DHCP info and then the Wi-Fi default gateway. Packets are sent
on a socket bound to the Wi-Fi network, so they still reach the radio when
Android prefers cellular because the mesh has no internet.

A manual address can be entered in the plugin pane (for example when testing
with a laptop); leave it blank for automatic.

## What is copied

* Own PLI: CoT type `a-*` whose UID is this device's UID.
* Own chat: CoT type `b-t-f` whose UID is `GeoChat.<own UID>.*`
  (read/delivered receipts `b-t-f-r` / `b-t-f-d` are not copied).

These are taken from two ATAK hooks (`CommsMapComponent` pre-send processor
and comms logger), de-duplicated. If no own PLI has been copied for 30 s
(ATAK reports rarely while stationary), the plugin builds one from the self
marker so the radio always has a recent position. The radio rate-limits what
actually goes over LoRa.

## Building

The project follows the layout of the SDK's `plugin-examples/plugintemplate`.

**Requirement: a 64-bit JDK 17 or newer to run Gradle.** The Android Gradle
Plugin 8.x (and Gradle 9) will not run on Java 8, and a 32-bit Java cannot
allocate the 4 GB heap set in `gradle.properties` ("Invalid maximum heap
size: -Xmx4g"). `settings.gradle` stops early with a clear message if Gradle
is on the wrong Java.

* Command line: point `JAVA_HOME` at a 64-bit JDK 17+ before running
  `gradlew`, then stop any old daemons. Android Studio's bundled JDK works:

  ```bat
  :: Windows cmd (PowerShell: $env:JAVA_HOME = "C:\Program Files\Android\Android Studio\jbr")
  set JAVA_HOME=C:\Program Files\Android\Android Studio\jbr
  gradlew --stop
  gradlew --version
  ```

  `gradlew --version` must show a 17+ "Daemon JVM" / "Launcher JVM", not
  `C:\Program Files (x86)\Java\jre1.8...`. To make it permanent, set
  `JAVA_HOME` under *System Properties → Environment Variables*, or put
  `org.gradle.java.home=C:/Program Files/Android/Android Studio/jbr` in
  `%USERPROFILE%\.gradle\gradle.properties`.
* Android Studio: *Settings → Build, Execution, Deployment → Build Tools →
  Gradle → Gradle JDK* → pick a 17+ JDK (the bundled *jbr-17*/*jbr-21* works).

1. Copy this `atak-plugin/` directory into your SDK as
   `<ATAK-CIV-SDK>/plugins/RarDualBand/` (the takdev Gradle plugin looks for
   the SDK two directories up).
2. Copy the Gradle wrapper (`gradlew`, `gradlew.bat`, `gradle/wrapper/`) from
   `<SDK>/plugin-examples/plugintemplate/` into it.
3. Create `local.properties` with `sdk.dir` and your signing keys, the same
   as for the template:

   ```properties
   sdk.dir=/path/to/Android/Sdk
   takDebugKeyFile=/path/to/debug.keystore
   takDebugKeyFilePassword=android
   takDebugKeyAlias=androiddebugkey
   takDebugKeyPassword=android
   ```
4. Build and install:

   ```sh
   ./gradlew assembleCivDebug
   adb install -r app/build/outputs/apk/civ/debug/*.apk
   ```
5. In ATAK: *Settings → Plugins* (or the Plugins tool), load **RAR Dual-Band**.

Use the template's `./gradlew`, not a system-installed `gradle`: the Android
Gradle Plugin only works with a matching Gradle version.

If your SDK's template uses different Android Gradle Plugin or takdev
versions than `app/build.gradle` (the `com.android.tools.build:gradle:` line
in its `app/build.gradle`), copy the template's `app/build.gradle` over this
one and keep `namespace 'com.rar.atak.dualband'`, `PLUGIN_VERSION`,
`ATAK_VERSION` and the `junit` test dependency.

Unit tests for the pure-Java parts (filter, PLI builder, de-duplication):

```sh
./gradlew testCivDebugUnitTest
```

## Using it

Tap the **RAR Dual-Band** toolbar icon to open the pane:

* **Always send over Meshtastic** — switches the copies to port 6701. The
  setting persists across restarts.
* **Radio address** — blank for automatic, or an IP/hostname override.
* **Status** — where copies are going, how many were sent, and the last error
  (for example "Not connected to the radio's Wi-Fi").

## Files

| File | Purpose |
|------|---------|
| `RarDualBandPlugin.java` | `IPlugin` entry point: toolbar item, pane, lifecycle |
| `OutboundTap.java` | ATAK send hooks; picks own PLI/chat and hands them to the sender |
| `OutboundFilter.java` | Pure-Java classification of outgoing CoT |
| `SelfPliFallback.java` | Builds a PLI from the self marker when ATAK has been quiet |
| `PliXml.java` | Pure-Java PLI CoT builder |
| `MeshSender.java` | Background UDP sender (6700/6701), Wi-Fi-bound socket, status |
| `RadioTarget.java` | Finds the radio's address (DHCP server / gateway / manual) |
| `RarSettings.java` | Persisted settings |
| `RarPaneController.java` | Pane UI binding |
