import { existsSync, writeFileSync } from 'fs';
import path from 'path';

// Written to src/openrig.json — only if one doesn't already exist,
// so a real provisioned config on disk is never overwritten.
const configPath = path.join(__dirname, '..', 'openrig.json');

const testConfig = {
  openrig: {
    device: {
      provisioned: true,
      type: 'hotspot',
      hostname: 'openrig-test',
      timezone: 'UTC',
    },
    operator: {
      callsign: 'N0CALL',
      name: 'Test',
      grid_square: 'EN52',
    },
    version: '0.1.0',
    hotspot: {
      rf_frequency: 433000000,
      tx_frequency: 0,
      modem: { type: 'mmdvm_hs_hat', port: '/dev/ttyAMA0' },
      dmr: {
        enabled: true,
        colorcode: 1,
        network: 'brandmeister',
        server: '',
        password: '',
        talkgroups: [],
      },
      ysf: {
        enabled: false,
        network: 'ysf',
        reflector: 'AMERICA',
        module: '',
        description: '',
      },
      cross_mode: {
        ysf2dmr_enabled: false,
        ysf2dmr_talkgroup: 0,
        dmr2ysf_enabled: false,
        dmr2ysf_room: '',
      },
    },
    network: { wifi: { networks: [] } },
    radio: { rigs: [] },
  },
};

export default async function globalSetup() {
  if (!existsSync(configPath)) {
    writeFileSync(configPath, JSON.stringify(testConfig, null, 2));
    console.log('[e2e setup] Created test openrig.json at', configPath);
  }
}
