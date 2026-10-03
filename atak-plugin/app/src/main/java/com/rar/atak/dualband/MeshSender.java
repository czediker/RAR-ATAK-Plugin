package com.rar.atak.dualband;

import android.content.Context;
import android.net.Network;
import android.util.Log;

import java.io.IOException;
import java.net.DatagramPacket;
import java.net.DatagramSocket;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.RejectedExecutionException;

/**
 * Sends copies of CoT events to the radio over UDP: port 6700 normally (the
 * radio forwards them over Meshtastic only when HaLow has no neighbors), or
 * port 6701 when "always send over Meshtastic" is on.
 */
final class MeshSender {

    static final int PORT_AUTO = 6700;
    static final int PORT_ALWAYS = 6701;

    private static final String TAG = "RarDualBand";

    private final RarSettings settings;
    private final RadioTarget target;
    private final ExecutorService executor = Executors.newSingleThreadExecutor(r -> {
        Thread t = new Thread(r, "rar-dualband-sender");
        t.setDaemon(true);
        return t;
    });

    // Touched only on the executor thread.
    private DatagramSocket socket;
    private Network socketNetwork;

    // Status for the pane.
    private volatile long sentCount;
    private volatile long lastSentAt;
    private volatile String lastDestination;
    private volatile String lastSource;
    private volatile String lastError;

    MeshSender(Context context, RarSettings settings) {
        this.settings = settings;
        this.target = new RadioTarget(context, settings);
    }

    /** Queue a CoT event for sending; never blocks the caller. */
    void send(final String cotXml) {
        if (cotXml == null) {
            return;
        }
        try {
            executor.execute(() -> sendNow(cotXml));
        } catch (RejectedExecutionException ignored) {
            // Plugin is stopping.
        }
    }

    void invalidateTarget() {
        target.invalidate();
    }

    void shutdown() {
        try {
            executor.execute(this::closeSocket);
        } catch (RejectedExecutionException ignored) {
            // Already stopped.
        }
        executor.shutdown();
    }

    long getSentCount() {
        return sentCount;
    }

    long getLastSentAt() {
        return lastSentAt;
    }

    String getLastDestination() {
        return lastDestination;
    }

    String getLastSource() {
        return lastSource;
    }

    String getLastError() {
        return lastError;
    }

    int currentPort() {
        return settings.isForceMeshtastic() ? PORT_ALWAYS : PORT_AUTO;
    }

    private void sendNow(String xml) {
        try {
            RadioTarget.Resolved r = target.resolve();
            if (r == null) {
                lastError = "Not connected to the radio's Wi-Fi";
                return;
            }
            int port = currentPort();
            byte[] data = xml.getBytes(StandardCharsets.UTF_8);
            socketFor(r.network).send(new DatagramPacket(data, data.length, r.address, port));
            sentCount++;
            lastSentAt = System.currentTimeMillis();
            lastDestination = r.address.getHostAddress() + ":" + port;
            lastSource = r.source;
            lastError = null;
        } catch (Exception e) {
            lastError = e.getClass().getSimpleName() + ": " + e.getMessage();
            Log.w(TAG, "send to radio failed", e);
            target.invalidate();
            closeSocket();
        }
    }

    /**
     * A socket bound to the Wi-Fi network, so packets reach the radio even
     * when Android prefers cellular because the mesh has no internet.
     */
    private DatagramSocket socketFor(Network network) throws IOException {
        if (socket != null && !socket.isClosed() && sameNetwork(network, socketNetwork)) {
            return socket;
        }
        closeSocket();
        DatagramSocket s = new DatagramSocket();
        if (network != null) {
            network.bindSocket(s);
        }
        socket = s;
        socketNetwork = network;
        return s;
    }

    private static boolean sameNetwork(Network a, Network b) {
        return a == null ? b == null : a.equals(b);
    }

    private void closeSocket() {
        if (socket != null) {
            socket.close();
            socket = null;
            socketNetwork = null;
        }
    }
}
