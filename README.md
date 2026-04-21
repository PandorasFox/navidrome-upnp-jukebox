# navidrome-jukebox

docker container that connects to a navidrome server w/ a provider user:pw, finds a local network uPNP receiver that fuzzy-matches the env-specified receiveer name, and then lets you queue up songs to it w/ gapless support.

supports searching by trackname, album name, or artist name, as well as full-album queueing / sifting through an artist's list of albums.

includes a toggle for queue-next vs queue-to-end.

<img width="762" height="683" alt="Screenshot 2026-04-20 at 23 51 22" src="https://github.com/user-attachments/assets/8730bfdc-a18b-48f8-b237-04fceabb239f" />

TODO:

- basic queue reordering
- cleaning up the player controls (play/pause/stop can be consolidated a bit into the playback info card)
- ?? idk it works pretty great honestly
