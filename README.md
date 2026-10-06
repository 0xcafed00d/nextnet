# nextnet

nextnet gives software running in the ZX Spectrum Next MiSTer core access to
MiSTer's network connection. Programs written for the Spectrum Next's ESP8266
Wi-Fi module can use the MiSTer's built-in network connection.

## Install

1. Download the nextnet release ZIP file and extract it on your computer.
2. Turn off MiSTer and put its SD card into your computer.
3. Open the root folder on the SD card.
4. Copy the complete extracted `nextnet-mister` folder to the root of the SD card. Keep all
   the files together. The SD card should then contain:

   ```text  
    nextnet-mister/
        nextnet
        install.sh
        uninstall.sh
   ```

5. Safely eject the SD card, put it back into MiSTer, and start MiSTer.
6. From the main MiSTer menu, open **Scripts**, then navigate to **nextnet-mister**, and run
   **install**.

The installer starts nextnet immediately and configures it to start
automatically whenever MiSTer boots. Leave the `nextnet-mister` folder in the
same place after installation.

MiSTer must already have a working network connection. nextnet uses that
connection, the type of connection MiSTer is using (Ethernet or Wi-Fi) does not matter.

## How it runs

nextnet runs quietly in the background as a daemon. It watches which MiSTer
core is running and only activates the serial and networking bridge while the
ZX Spectrum Next core is active. When you leave the Next core, nextnet closes
the connection and returns to its idle state. It becomes active again the next
time the Next core starts.

## Update

Replace the files in `nextnet-mister` with those from the new release,
run **install** again from the MiSTer Scripts menu, and then restart MiSTer.

## Uninstall

From the MiSTer Scripts menu, open **nextnet-mister** and run **uninstall**.
This stops nextnet and removes its automatic startup entry. You can then delete
the `nextnet-mister` folder from the SD card.

## This has been tested with the following ZX Spectrum Next network programs:
- http dot command
- NXtel 
- getit 
- zxdb-dl
- Terminex II v1.51
- NextBench - fileserve/update/getit.

Raise an issue if you encounter any Next network programs that do not work with nextnet.

## Note 
In order for nextnet to reset correctly, it needs a change to the ZX Spectrum Next core. This change is pending and will be included in a future core update.
In the meantime, the update to the Spectrum Next core is included in the nextnet release package itself.
Simply copy the RBF file into the '_Unstable' folder on the on the root of the SD card, and start this core from the MiSTer menu, until the updated core is released..
If not using this updated core, nextnet may not reset correctly. Until then if the nextnet gets into an incorrect state, you may need to force a reset by closing the Next core and restarting it.

## Version History 

### v0.2.0
- Added support for incoming network connections
- Tested with various NextBench applications
- Implemented activity timeout for network connections
- Included updated Spectrum Next core - for resetting nextnet correctly
### v0.1.0
- Initial public release

