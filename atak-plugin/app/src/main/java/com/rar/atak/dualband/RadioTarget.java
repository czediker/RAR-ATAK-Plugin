package com.rar.atak.dualband;

import android.content.Context;
import android.net.ConnectivityManager;
import android.net.DhcpInfo;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.RouteInfo;
import android.net.wifi.WifiManager;
import android.os.Build;

import java.net.Inet4Address;
import java.net.InetAddress;
import java.net.UnknownHostException;

/**
 * Finds the radio (Raspberry Pi) without a configured IP address: the phone
 * is on the radio's Wi-Fi access point and every openMANET node runs its
 * own DHCP server, so the DHCP server that leased the phone its address is
 * the phone's own radio. Falls back to the Wi-Fi default gateway, and a
 * manual address in settings overrides both.
 */
final class RadioTarget {

    /** A resolved destination and the Wi-Fi network to send it over. */
    static final class Resolved {
        final InetAddress address;
        final Network network;
        final String source;

        Resolved(InetAddress address, Network network, String source) {
            this.address = address;
            this.network = network;
            this.source = source;
        }
    }

    private static final long CACHE_MS = 10_000;

    private final Context context;
    private final RarSettings settings;
    private Resolved cached;
    private long cachedAt;

    RadioTarget(Context context, RarSettings settings) {
        this.context = context.getApplicationContext();
        this.settings = settings;
    }

    /** Forget the cached result (after a settings change or send error). */
    synchronized void invalidate() {
        cached = null;
    }

    /** Returns the radio address, or null when there is no Wi-Fi network. */
    synchronized Resolved resolve() throws UnknownHostException {
        long now = System.currentTimeMillis();
        if (cached != null && now - cachedAt < CACHE_MS) {
            return cached;
        }
        cached = lookup();
        cachedAt = now;
        return cached;
    }

    private Resolved lookup() throws UnknownHostException {
        ConnectivityManager cm = (ConnectivityManager) context.getSystemService(Context.CONNECTIVITY_SERVICE);
        Network wifi = cm == null ? null : findWifi(cm);

        String manual = settings.getRadioAddress();
        if (!manual.isEmpty()) {
            return new Resolved(InetAddress.getByName(manual), wifi, "manual");
        }
        if (wifi == null) {
            return null;
        }
        LinkProperties lp = cm.getLinkProperties(wifi);
        if (Build.VERSION.SDK_INT >= 30 && lp != null) {
            Inet4Address dhcp = lp.getDhcpServerAddress();
            if (dhcp != null && !dhcp.isAnyLocalAddress()) {
                return new Resolved(dhcp, wifi, "DHCP server");
            }
        }
        WifiManager wm = (WifiManager) context.getSystemService(Context.WIFI_SERVICE);
        if (wm != null) {
            @SuppressWarnings("deprecation")
            DhcpInfo info = wm.getDhcpInfo();
            if (info != null && info.serverAddress != 0) {
                return new Resolved(fromLittleEndian(info.serverAddress), wifi, "DHCP server");
            }
        }
        if (lp != null) {
            for (RouteInfo r : lp.getRoutes()) {
                if (r.isDefaultRoute() && r.getGateway() instanceof Inet4Address) {
                    return new Resolved(r.getGateway(), wifi, "gateway");
                }
            }
        }
        return null;
    }

    @SuppressWarnings("deprecation")
    private static Network findWifi(ConnectivityManager cm) {
        for (Network n : cm.getAllNetworks()) {
            NetworkCapabilities caps = cm.getNetworkCapabilities(n);
            if (caps != null && caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) {
                return n;
            }
        }
        return null;
    }

    private static InetAddress fromLittleEndian(int a) throws UnknownHostException {
        return InetAddress.getByAddress(new byte[] {
                (byte) a, (byte) (a >> 8), (byte) (a >> 16), (byte) (a >> 24)});
    }
}
