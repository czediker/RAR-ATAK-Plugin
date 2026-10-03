package com.rar.atak.dualband;

/**
 * Decides which outgoing CoT events are copied to the radio: this device's
 * own position reports and the GeoChat messages it sends. Pure Java so it
 * can be unit tested without ATAK.
 */
public final class OutboundFilter {

    public enum Kind { NONE, PLI, CHAT }

    /** CoT type of a GeoChat message (receipts use b-t-f-d / b-t-f-r). */
    static final String CHAT_TYPE = "b-t-f";

    private OutboundFilter() {
    }

    /**
     * @param type    CoT event type
     * @param uid     CoT event UID
     * @param selfUid this device's ATAK UID
     */
    public static Kind classify(String type, String uid, String selfUid) {
        if (type == null || uid == null || selfUid == null || selfUid.isEmpty()) {
            return Kind.NONE;
        }
        // GeoChat UIDs are "GeoChat.<senderUid>.<chatroom>.<messageId>".
        if (CHAT_TYPE.equals(type) && uid.startsWith("GeoChat." + selfUid + ".")) {
            return Kind.CHAT;
        }
        if (type.startsWith("a-") && uid.equals(selfUid)) {
            return Kind.PLI;
        }
        return Kind.NONE;
    }
}
