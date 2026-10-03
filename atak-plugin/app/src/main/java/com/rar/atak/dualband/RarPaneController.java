package com.rar.atak.dualband;

import android.content.Context;
import android.view.View;
import android.widget.Button;
import android.widget.CompoundButton;
import android.widget.EditText;
import android.widget.Switch;
import android.widget.TextView;

/** Binds the plugin pane: the Meshtastic toggle, radio address and status. */
final class RarPaneController {

    private static final long REFRESH_MS = 2000;

    private final Context pluginContext;
    private final View root;
    private final RarSettings settings;
    private final MeshSender sender;
    private final Switch forceSwitch;
    private final EditText addressEdit;
    private final TextView modeText;
    private final TextView statusText;
    private final Runnable refresher = new Runnable() {
        @Override
        public void run() {
            refresh();
            if (root.isAttachedToWindow()) {
                root.postDelayed(this, REFRESH_MS);
            }
        }
    };

    RarPaneController(Context pluginContext, View root, RarSettings settings, MeshSender sender) {
        this.pluginContext = pluginContext;
        this.root = root;
        this.settings = settings;
        this.sender = sender;

        forceSwitch = root.findViewById(R.id.rar_force_switch);
        addressEdit = root.findViewById(R.id.rar_address_edit);
        modeText = root.findViewById(R.id.rar_mode_text);
        statusText = root.findViewById(R.id.rar_status_text);
        Button save = root.findViewById(R.id.rar_address_save);

        forceSwitch.setChecked(settings.isForceMeshtastic());
        forceSwitch.setOnCheckedChangeListener(new CompoundButton.OnCheckedChangeListener() {
            @Override
            public void onCheckedChanged(CompoundButton button, boolean checked) {
                settings.setForceMeshtastic(checked);
                refresh();
            }
        });
        addressEdit.setText(settings.getRadioAddress());
        save.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                settings.setRadioAddress(addressEdit.getText().toString());
                sender.invalidateTarget();
                refresh();
            }
        });
        root.addOnAttachStateChangeListener(new View.OnAttachStateChangeListener() {
            @Override
            public void onViewAttachedToWindow(View v) {
                root.removeCallbacks(refresher);
                root.post(refresher);
            }

            @Override
            public void onViewDetachedFromWindow(View v) {
                root.removeCallbacks(refresher);
            }
        });
    }

    void refresh() {
        boolean force = settings.isForceMeshtastic();
        modeText.setText(pluginContext.getString(force ? R.string.rar_mode_always : R.string.rar_mode_auto,
                sender.currentPort()));

        StringBuilder sb = new StringBuilder();
        String dest = sender.getLastDestination();
        if (dest != null) {
            sb.append(pluginContext.getString(R.string.rar_status_radio, dest, sender.getLastSource()));
        } else {
            sb.append(pluginContext.getString(R.string.rar_status_no_radio));
        }
        sb.append('\n');
        long last = sender.getLastSentAt();
        String ago = last == 0 ? "-" : ((System.currentTimeMillis() - last) / 1000) + " s";
        sb.append(pluginContext.getString(R.string.rar_status_sent, sender.getSentCount(), ago));
        String err = sender.getLastError();
        if (err != null) {
            sb.append('\n').append(pluginContext.getString(R.string.rar_status_error, err));
        }
        statusText.setText(sb.toString());
    }

    void dispose() {
        root.removeCallbacks(refresher);
    }
}
