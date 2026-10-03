package com.rar.atak.dualband;

import android.content.Context;
import android.content.SharedPreferences;
import android.preference.PreferenceManager;

/** Plugin settings, stored in ATAK's shared preferences. */
final class RarSettings {

    private static final String KEY_FORCE = "rar_dualband_force_meshtastic";
    private static final String KEY_ADDRESS = "rar_dualband_radio_address";

    private final SharedPreferences prefs;

    RarSettings(Context atakContext) {
        prefs = PreferenceManager.getDefaultSharedPreferences(atakContext);
    }

    /** True when every chat/position copy goes to port 6701 (always Meshtastic). */
    boolean isForceMeshtastic() {
        return prefs.getBoolean(KEY_FORCE, false);
    }

    void setForceMeshtastic(boolean force) {
        prefs.edit().putBoolean(KEY_FORCE, force).apply();
    }

    /** Manual radio address; empty means discover it automatically. */
    String getRadioAddress() {
        String a = prefs.getString(KEY_ADDRESS, "");
        return a == null ? "" : a.trim();
    }

    void setRadioAddress(String address) {
        prefs.edit().putString(KEY_ADDRESS, address == null ? "" : address.trim()).apply();
    }
}
