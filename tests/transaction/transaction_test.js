// Import
const { ApiPromise, WsProvider, HttpProvider} = require('@polkadot/api');
//const {Keyring } = require('@polkadot/keyring');

// 7001/tcp 8545/tcp 8546/tcp 8540/tcp 9876/tcp 6060/tcp
//172.19.0.2
async function main () {
    // Construct
    //const wsProvider = new WsProvider('wss://rpc.polkadot.io');
    const wsProvider = new WsProvider('ws://127.0.0.1:8546');

    // Create the instance
    const api = new ApiPromise({ provider: wsProvider });

// Wait until we are ready and connected
    await api.isReady;

// Do something
    console.log(api.genesisHash.toHex());


    const runtimeVersion = await api.runtimeVersion;
    console.log(`runtime version: ${runtimeVersion}`);
}

main().then(() => console.log('completed'))