package com.rar.atak.dualband;

import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.content.SharedPreferences;
import android.os.BatteryManager;
import android.os.Handler;
import android.os.Looper;
import android.preference.PreferenceManager;
import android.util.Log;

import com.atakmap.android.maps.MapView;
import com.atakmap.android.maps.Marker;
import com.atakmap.coremap.maps.coords.GeoPoint;

/**
 * If no own position report has been copied to the radio for a while (ATAK
 * reports rarely while stationary, or its SA path bypassed the hooks),
 * builds one from the self marker. This keeps the radio's position for this
 * user fresh; the radio rate-limits what actually goes over Meshtastic.
 */
final class SelfPliFallback implements Runnable {

    private static final String TAG = "RarDualBand";
    private static final long CHECK_MS = 10_000;
    private static final long QUIET_MS = 30_000;

    private final MapView mapView;
    private final MeshSender sender;
    private final OutboundTap tap;
    private final Handler handler = new Handler(Looper.getMainLooper());

    SelfPliFallback(MapView mapView, MeshSender sender, OutboundTap tap) {
        this.mapView = mapView;
        this.sender = sender;
        this.tap = tap;
    }

    void start() {
        handler.postDelayed(this, CHECK_MS);
    }

    void stop() {
        handler.removeCallbacks(this);
    }

    @Override
    public void run() {
        try {
            if (System.currentTimeMillis() - tap.getLastSelfPliAt() > QUIET_MS) {
                String xml = buildFromSelfMarker();
                if (xml != null) {
                    sender.send(xml);
                    tap.noteSelfPli();
                }
            }
        } catch (RuntimeException e) {
            Log.w(TAG, "self PLI fallback failed", e);
        }
        handler.postDelayed(this, CHECK_MS);
    }

    private String buildFromSelfMarker() {
        Marker self = mapView.getSelfMarker();
        if (self == null) {
            return null;
        }
        GeoPoint p = self.getPoint();
        if (p == null || !p.isValid() || (p.getLatitude() == 0 && p.getLongitude() == 0)) {
            return null;
        }
        Context ctx = mapView.getContext();
        SharedPreferences prefs = PreferenceManager.getDefaultSharedPreferences(ctx);

        PliXml.Pli pli = new PliXml.Pli();
        pli.uid = MapView.getDeviceUid();
        pli.callsign = mapView.getDeviceCallsign();
        pli.lat = p.getLatitude();
        pli.lon = p.getLongitude();
        pli.hae = p.isAltitudeValid() ? p.getAltitude() : Double.NaN;
        pli.team = prefs.getString("locationTeam", "Cyan");
        pli.role = prefs.getString("atakRoleType", "Team Member");
        pli.battery = batteryPercent(ctx);
        pli.speed = self.getMetaDouble("Speed", Double.NaN);
        pli.course = self.getTrackHeading();
        return PliXml.build(pli, System.currentTimeMillis());
    }

    private static int batteryPercent(Context ctx) {
        Intent b = ctx.registerReceiver(null, new IntentFilter(Intent.ACTION_BATTERY_CHANGED));
        if (b == null) {
            return -1;
        }
        int level = b.getIntExtra(BatteryManager.EXTRA_LEVEL, -1);
        int scale = b.getIntExtra(BatteryManager.EXTRA_SCALE, -1);
        return level >= 0 && scale > 0 ? Math.round(100f * level / scale) : -1;
    }
}
