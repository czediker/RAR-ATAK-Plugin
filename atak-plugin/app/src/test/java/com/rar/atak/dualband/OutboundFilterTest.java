package com.rar.atak.dualband;

import static org.junit.Assert.assertEquals;

import org.junit.Test;

public class OutboundFilterTest {

    private static final String SELF = "ANDROID-589520ccfcd20f01";

    @Test
    public void ownPositionReportIsPli() {
        assertEquals(OutboundFilter.Kind.PLI, OutboundFilter.classify("a-f-G-U-C", SELF, SELF));
        assertEquals(OutboundFilter.Kind.PLI, OutboundFilter.classify("a-f-G-E-V-C", SELF, SELF));
    }

    @Test
    public void otherUsersAndMarkersAreIgnored() {
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("a-f-G-U-C", "ANDROID-other", SELF));
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("a-h-G", "marker-uuid", SELF));
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("b-m-p-s-p-i", SELF, SELF));
    }

    @Test
    public void ownChatIsChat() {
        assertEquals(OutboundFilter.Kind.CHAT, OutboundFilter.classify("b-t-f",
                "GeoChat." + SELF + ".All Chat Rooms.5d1f2a3b-1111-2222-3333-444455556666", SELF));
        assertEquals(OutboundFilter.Kind.CHAT, OutboundFilter.classify("b-t-f",
                "GeoChat." + SELF + ".ANDROID-bravo.msg", SELF));
    }

    @Test
    public void receiptsAndOthersChatsAreIgnored() {
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("b-t-f-d", "GeoChat." + SELF + ".x.y", SELF));
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("b-t-f-r", "GeoChat." + SELF + ".x.y", SELF));
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("b-t-f", "GeoChat.ANDROID-bravo.All Chat Rooms.m", SELF));
        // A UID that merely starts with ours is not ours.
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("b-t-f", "GeoChat." + SELF + "x.All Chat Rooms.m", SELF));
    }

    @Test
    public void nullsAreIgnored() {
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify(null, SELF, SELF));
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("a-f", null, SELF));
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("a-f", SELF, null));
        assertEquals(OutboundFilter.Kind.NONE, OutboundFilter.classify("a-f", "", ""));
    }
}
