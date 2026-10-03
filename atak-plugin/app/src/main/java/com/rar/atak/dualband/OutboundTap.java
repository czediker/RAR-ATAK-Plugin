package com.rar.atak.dualband;

import android.util.Log;

import com.atakmap.android.maps.MapView;
import com.atakmap.comms.CommsLogger;
import com.atakmap.comms.CommsMapComponent;
import com.atakmap.coremap.cot.event.CotEvent;

/**
 * Watches CoT that ATAK sends and copies this device's own position reports
 * and GeoChat messages to the radio. ATAK keeps sending them normally (SA
 * multicast on 239.2.3.1:6969, chat on 224.10.10.1:17012 or direct); the
 * radio decides whether its copy also goes out over Meshtastic.
 *
 * Two hooks are registered because ATAK does not route every send through
 * the same one: the pre-send processor sees events sent to contacts, and
 * the comms logger sees broadcast sends. Duplicates are dropped.
 */
final class OutboundTap implements CommsMapComponent.PreSendProcessor, CommsLogger {

    private static final String TAG = "RarDualBand";

    private final MeshSender sender;
    private final RecentKeys recent = new RecentKeys(128);
    private volatile long lastSelfPliAt;
    private boolean registered;

    OutboundTap(MeshSender sender) {
        this.sender = sender;
    }

    void register() {
        CommsMapComponent cmc = CommsMapComponent.getInstance();
        if (cmc == null) {
            Log.e(TAG, "CommsMapComponent not available; outbound tap disabled");
            return;
        }
        cmc.registerPreSendProcessor(this);
        cmc.registerCommsLogger(this);
        registered = true;
    }

    void unregister() {
        CommsMapComponent cmc = CommsMapComponent.getInstance();
        if (cmc != null && registered) {
            cmc.unregisterPreSendProcessor(this);
            cmc.unregisterCommsLogger(this);
        }
        registered = false;
    }

    /** Time of the last own PLI copied to the radio (ms since epoch). */
    long getLastSelfPliAt() {
        return lastSelfPliAt;
    }

    /** Called by the fallback after it sends a synthesized PLI. */
    void noteSelfPli() {
        lastSelfPliAt = System.currentTimeMillis();
    }

    // CommsMapComponent.PreSendProcessor

    @Override
    public void processCotEvent(CotEvent event, String[] toUIDs) {
        handle(event);
    }

    // CommsLogger

    @Override
    public void logSend(CotEvent msg, String destination) {
        handle(msg);
    }

    @Override
    public void logSend(CotEvent msg, String[] toUIDs) {
        handle(msg);
    }

    @Override
    public void logReceive(CotEvent msg, String rxid, String server) {
        // Inbound traffic is not copied.
    }

    @Override
    public void dispose() {
    }

    private void handle(CotEvent event) {
        try {
            if (event == null || !event.isValid()) {
                return;
            }
            OutboundFilter.Kind kind = OutboundFilter.classify(
                    event.getType(), event.getUID(), MapView.getDeviceUid());
            if (kind == OutboundFilter.Kind.NONE) {
                return;
            }
            String key = event.getUID() + "|" + event.getTime().getMilliseconds();
            if (!recent.add(key)) {
                return;
            }
            if (kind == OutboundFilter.Kind.PLI) {
                lastSelfPliAt = System.currentTimeMillis();
            }
            sender.send(event.toString());
        } catch (RuntimeException e) {
            // Never let the plugin break ATAK's send path.
            Log.w(TAG, "outbound tap error", e);
        }
    }
}
