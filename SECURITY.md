# Security

wasim is a development tool. It is not designed to face the internet.

- **The console has no authentication.** Anyone who reaches its port can read all
  conversations and drive the simulator. Keep it on localhost or a private network.
- **Do not use real credentials.** wasim never contacts Meta and needs no real app
  secret or access token. Use throwaway values.
- **Do not store real personal data.** Conversations and media are stored unencrypted
  in the SQLite file and media directory.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting on this repository rather than
a public issue. Include the steps to reproduce and the affected version.
