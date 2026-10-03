package com.rar.atak.dualband;

import android.content.Context;
import android.view.View;

import com.atak.plugins.impl.PluginContextProvider;
import com.atak.plugins.impl.PluginLayoutInflater;
import com.atakmap.android.maps.MapView;

import gov.tak.api.plugin.IPlugin;
import gov.tak.api.plugin.IServiceController;
import gov.tak.api.ui.IHostUIService;
import gov.tak.api.ui.Pane;
import gov.tak.api.ui.PaneBuilder;
import gov.tak.api.ui.ToolbarItem;
import gov.tak.api.ui.ToolbarItemAdapter;
import gov.tak.platform.marshal.MarshalManager;

/**
 * RAR dual-band plugin entry point (declared in assets/plugin.xml).
 *
 * Copies this device's position reports and outgoing GeoChat messages to
 * the radio on UDP 6700 (or 6701 when "always send over Meshtastic" is on).
 * The plugin only sends; traffic received over Meshtastic is delivered to
 * ATAK by the radio directly.
 */
public class RarDualBandPlugin implements IPlugin {

    private final Context pluginContext;
    private final IHostUIService uiService;
    private final ToolbarItem toolbarItem;

    private RarSettings settings;
    private MeshSender sender;
    private OutboundTap tap;
    private SelfPliFallback fallback;
    private Pane pane;
    private RarPaneController paneController;

    public RarDualBandPlugin(IServiceController serviceController) {
        final PluginContextProvider ctxProvider = serviceController.getService(PluginContextProvider.class);
        pluginContext = ctxProvider.getPluginContext();
        pluginContext.setTheme(R.style.ATAKPluginTheme);
        uiService = serviceController.getService(IHostUIService.class);

        toolbarItem = new ToolbarItem.Builder(
                pluginContext.getString(R.string.app_name),
                MarshalManager.marshal(
                        pluginContext.getResources().getDrawable(R.drawable.ic_launcher),
                        android.graphics.drawable.Drawable.class,
                        gov.tak.api.commons.graphics.Bitmap.class))
                .setListener(new ToolbarItemAdapter() {
                    @Override
                    public void onClick(ToolbarItem item) {
                        showPane();
                    }
                })
                .build();
    }

    @Override
    public void onStart() {
        MapView mapView = MapView.getMapView();
        Context atakContext = mapView.getContext();
        settings = new RarSettings(atakContext);
        sender = new MeshSender(atakContext, settings);
        tap = new OutboundTap(sender);
        tap.register();
        fallback = new SelfPliFallback(mapView, sender, tap);
        fallback.start();
        if (uiService != null) {
            uiService.addToolbarItem(toolbarItem);
        }
    }

    @Override
    public void onStop() {
        if (uiService != null) {
            uiService.removeToolbarItem(toolbarItem);
        }
        if (paneController != null) {
            paneController.dispose();
        }
        if (fallback != null) {
            fallback.stop();
        }
        if (tap != null) {
            tap.unregister();
        }
        if (sender != null) {
            sender.shutdown();
        }
    }

    private void showPane() {
        if (uiService == null || settings == null) {
            return;
        }
        if (pane == null) {
            View view = PluginLayoutInflater.inflate(pluginContext, R.layout.rar_pane, null);
            paneController = new RarPaneController(pluginContext, view, settings, sender);
            pane = new PaneBuilder(view)
                    .setMetaValue(Pane.RELATIVE_LOCATION, Pane.Location.Default)
                    .setMetaValue(Pane.PREFERRED_WIDTH_RATIO, 0.5D)
                    .setMetaValue(Pane.PREFERRED_HEIGHT_RATIO, 0.5D)
                    .build();
        }
        paneController.refresh();
        if (!uiService.isPaneVisible(pane)) {
            uiService.showPane(pane, null);
        }
    }
}
