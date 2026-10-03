package com.rar.atak.dualband;

import java.util.LinkedHashMap;
import java.util.Map;

/**
 * A small bounded set of recently seen keys. ATAK can report the same
 * outgoing event through more than one hook; this keeps each event from
 * being copied to the radio twice.
 */
final class RecentKeys {

    private final Map<String, Boolean> keys;

    RecentKeys(final int capacity) {
        keys = new LinkedHashMap<String, Boolean>(capacity, 0.75f, false) {
            @Override
            protected boolean removeEldestEntry(Map.Entry<String, Boolean> eldest) {
                return size() > capacity;
            }
        };
    }

    /** Returns true if the key was not seen recently (and records it). */
    synchronized boolean add(String key) {
        return keys.put(key, Boolean.TRUE) == null;
    }
}
