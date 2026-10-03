package com.rar.atak.dualband;

import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import java.util.TimeZone;

/**
 * Builds a minimal ATAK position report (PLI). Used when ATAK has not sent
 * one through the hooked send path for a while (for example while
 * stationary with a long reporting interval), so the radio still has a
 * recent position to forward. Pure Java so it can be unit tested.
 */
public final class PliXml {

    /** Position report fields. NaN / negative values mean "unknown". */
    public static final class Pli {
        public String uid;
        public String callsign;
        public String type = "a-f-G-U-C";
        public double lat;
        public double lon;
        public double hae = Double.NaN;
        public String team;
        public String role;
        public int battery = -1;
        public double speed = Double.NaN;
        public double course = Double.NaN;
        public long staleMillis = 5 * 60 * 1000L;
    }

    private static final double UNKNOWN = 9999999.0;

    private PliXml() {
    }

    public static String build(Pli p, long nowMillis) {
        String time = isoTime(nowMillis);
        String stale = isoTime(nowMillis + p.staleMillis);
        StringBuilder sb = new StringBuilder(512);
        sb.append("<?xml version=\"1.0\" encoding=\"UTF-8\" standalone=\"yes\"?>");
        sb.append("<event version=\"2.0\"");
        attr(sb, "uid", p.uid);
        attr(sb, "type", p.type);
        attr(sb, "how", "m-g");
        attr(sb, "time", time);
        attr(sb, "start", time);
        attr(sb, "stale", stale);
        sb.append('>');
        sb.append("<point");
        attr(sb, "lat", Double.toString(p.lat));
        attr(sb, "lon", Double.toString(p.lon));
        attr(sb, "hae", Double.toString(Double.isNaN(p.hae) ? UNKNOWN : p.hae));
        attr(sb, "ce", Double.toString(UNKNOWN));
        attr(sb, "le", Double.toString(UNKNOWN));
        sb.append("/><detail>");
        if (p.callsign != null) {
            sb.append("<contact");
            attr(sb, "callsign", p.callsign);
            sb.append("/><uid");
            attr(sb, "Droid", p.callsign);
            sb.append("/>");
        }
        if (p.team != null || p.role != null) {
            sb.append("<__group");
            attr(sb, "name", p.team != null ? p.team : "Cyan");
            attr(sb, "role", p.role != null ? p.role : "Team Member");
            sb.append("/>");
        }
        if (p.battery >= 0) {
            sb.append("<status");
            attr(sb, "battery", Integer.toString(p.battery));
            sb.append("/>");
        }
        if (!Double.isNaN(p.speed) || !Double.isNaN(p.course)) {
            sb.append("<track");
            attr(sb, "speed", Double.toString(Double.isNaN(p.speed) ? 0 : p.speed));
            attr(sb, "course", Double.toString(Double.isNaN(p.course) ? 0 : p.course));
            sb.append("/>");
        }
        sb.append("</detail></event>");
        return sb.toString();
    }

    static String isoTime(long millis) {
        SimpleDateFormat f = new SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US);
        f.setTimeZone(TimeZone.getTimeZone("UTC"));
        return f.format(new Date(millis));
    }

    private static void attr(StringBuilder sb, String name, String value) {
        if (value == null) {
            return;
        }
        sb.append(' ').append(name).append("=\"");
        for (int i = 0; i < value.length(); i++) {
            char c = value.charAt(i);
            switch (c) {
                case '&': sb.append("&amp;"); break;
                case '<': sb.append("&lt;"); break;
                case '>': sb.append("&gt;"); break;
                case '"': sb.append("&quot;"); break;
                case '\'': sb.append("&apos;"); break;
                default: sb.append(c);
            }
        }
        sb.append('"');
    }
}
