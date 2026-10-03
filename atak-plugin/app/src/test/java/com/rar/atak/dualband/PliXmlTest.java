package com.rar.atak.dualband;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.io.ByteArrayInputStream;
import java.nio.charset.StandardCharsets;

import javax.xml.parsers.DocumentBuilderFactory;

import org.junit.Test;
import org.w3c.dom.Document;
import org.w3c.dom.Element;

public class PliXmlTest {

    private static final long NOW = 1791028800000L; // 2026-10-03T12:00:00Z

    private static Document parse(String xml) throws Exception {
        return DocumentBuilderFactory.newInstance().newDocumentBuilder()
                .parse(new ByteArrayInputStream(xml.getBytes(StandardCharsets.UTF_8)));
    }

    @Test
    public void buildsFullPli() throws Exception {
        PliXml.Pli p = new PliXml.Pli();
        p.uid = "ANDROID-1";
        p.callsign = "AL\"PHA & <co>";
        p.lat = 38.8977;
        p.lon = -77.0365;
        p.hae = 25.5;
        p.team = "Cyan";
        p.role = "Team Lead";
        p.battery = 88;
        p.speed = 1.5;
        p.course = 270;
        Document d = parse(PliXml.build(p, NOW));

        Element ev = d.getDocumentElement();
        assertEquals("event", ev.getTagName());
        assertEquals("ANDROID-1", ev.getAttribute("uid"));
        assertEquals("a-f-G-U-C", ev.getAttribute("type"));
        assertEquals("2026-10-03T12:00:00.000Z", ev.getAttribute("time"));
        assertEquals("2026-10-03T12:05:00.000Z", ev.getAttribute("stale"));
        Element pt = (Element) ev.getElementsByTagName("point").item(0);
        assertEquals(38.8977, Double.parseDouble(pt.getAttribute("lat")), 1e-9);
        assertEquals(25.5, Double.parseDouble(pt.getAttribute("hae")), 1e-9);
        Element contact = (Element) ev.getElementsByTagName("contact").item(0);
        assertEquals("AL\"PHA & <co>", contact.getAttribute("callsign"));
        Element group = (Element) ev.getElementsByTagName("__group").item(0);
        assertEquals("Team Lead", group.getAttribute("role"));
        assertEquals("88", ((Element) ev.getElementsByTagName("status").item(0)).getAttribute("battery"));
        assertEquals(270, Double.parseDouble(((Element) ev.getElementsByTagName("track").item(0)).getAttribute("course")), 1e-9);
    }

    @Test
    public void omitsUnknowns() throws Exception {
        PliXml.Pli p = new PliXml.Pli();
        p.uid = "ANDROID-1";
        p.lat = 1;
        p.lon = 2;
        String xml = PliXml.build(p, NOW);
        Document d = parse(xml);
        Element pt = (Element) d.getElementsByTagName("point").item(0);
        assertEquals(9999999.0, Double.parseDouble(pt.getAttribute("hae")), 0);
        assertFalse(xml.contains("<status"));
        assertFalse(xml.contains("<track"));
        assertFalse(xml.contains("<contact"));
        assertTrue(xml.contains("<detail>"));
    }
}
