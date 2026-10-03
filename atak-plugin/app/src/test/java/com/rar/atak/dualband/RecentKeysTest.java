package com.rar.atak.dualband;

import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import org.junit.Test;

public class RecentKeysTest {

    @Test
    public void rejectsDuplicatesAndEvictsOldest() {
        RecentKeys k = new RecentKeys(2);
        assertTrue(k.add("a"));
        assertFalse(k.add("a"));
        assertTrue(k.add("b"));
        assertTrue(k.add("c")); // evicts "a"
        assertTrue(k.add("a"));
        assertFalse(k.add("c"));
    }
}
