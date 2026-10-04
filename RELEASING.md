# Releasing SuperDirectory

A release starts when a version tag reaches GitHub. The `Release` workflow (`.github/workflows/release.yml`) then runs GoReleaser (`.goreleaser.yaml`), which:

1. Builds `superdirectory` for macOS, Linux and Windows, on Intel and ARM: six binaries.
2. Signs and notarizes the two macOS binaries, when the signing secrets exist.
3. Packs each binary with `README.md` and `LICENSE` (`.tar.gz`; `.zip` for Windows), and writes `checksums.txt`.
4. Publishes a GitHub release with the archives and a changelog from the commits.
5. Updates the Homebrew cask in `ozzyphantom/homebrew-tap`, when the tap token exists.

Each secret switches on one part. Without any of them, a tag still produces a complete GitHub release.

| Secret | Switches on |
|---|---|
| `HOMEBREW_TAP_GITHUB_TOKEN` | Step 5, the Homebrew cask |
| `MACOS_SIGN_P12`, `MACOS_SIGN_PASSWORD` | Step 2, signing |
| `MACOS_NOTARY_KEY`, `MACOS_NOTARY_KEY_ID`, `MACOS_NOTARY_ISSUER_ID` | Step 2, notarization |

## One-time setup

### 1. Create the tap

1. Run `gh repo create ozzyphantom/homebrew-tap --public --description "Homebrew tap for SuperDirectory"`.
2. Leave it empty. GoReleaser writes `Casks/superdirectory.rb` on the first release.

### 2. Give the release workflow access to the tap

1. On GitHub, open **Settings → Developer settings → Personal access tokens → Fine-grained tokens**.
2. Select **Generate new token**.
3. Set **Repository access** to **Only select repositories**, and select `ozzyphantom/homebrew-tap`.
4. Set **Permissions → Contents** to **Read and write**.
5. Generate the token and copy it.
6. Run `gh secret set HOMEBREW_TAP_GITHUB_TOKEN --repo ozzyphantom/SuperDirectory`, and paste the token when asked.

### 3. Sign and notarize the macOS builds

You need the Apple Developer membership. Signing uses a **Developer ID Application** certificate. Notarization uses an **App Store Connect API key**.

1. In **Keychain Access**, find the **Developer ID Application** certificate. If there is none, create one at developer.apple.com → **Certificates**.
2. Export the certificate with its private key as a `.p12` file. Set a password.
3. Run `base64 -i DeveloperID.p12 | gh secret set MACOS_SIGN_P12 --repo ozzyphantom/SuperDirectory`.
4. Run `gh secret set MACOS_SIGN_PASSWORD --repo ozzyphantom/SuperDirectory`, and enter the `.p12` password.
5. In App Store Connect, open **Users and Access → Integrations → App Store Connect API**. Generate a key with the **Developer** role.
6. Download the `.p8` key file. Apple allows one download only.
7. Run `base64 -i AuthKey_XXXXXXXXXX.p8 | gh secret set MACOS_NOTARY_KEY --repo ozzyphantom/SuperDirectory`.
8. Run `gh secret set MACOS_NOTARY_KEY_ID --repo ozzyphantom/SuperDirectory`, and enter the key ID.
9. Run `gh secret set MACOS_NOTARY_ISSUER_ID --repo ozzyphantom/SuperDirectory`, and enter the issuer ID from the same page.
10. Move the `.p12` and `.p8` files into your password manager, and delete the loose copies.

## Each release

1. Make sure the `CI` workflow passes on `main`.
2. Choose the version. Use `v0.x.y` until the interface is settled.
3. Run `git tag -a v0.1.0 -m "SuperDirectory v0.1.0"`.
4. Run `git push origin v0.1.0`.
5. Run `gh run watch --repo ozzyphantom/SuperDirectory`, and wait for the `Release` workflow to finish.
6. Check the release page: six archives and `checksums.txt`.
7. Check the cask: run `brew install ozzyphantom/tap/superdirectory`, then `superdirectory --version`.
8. Check the signature on a Mac: run `codesign --verify --verbose "$(which superdirectory)"`.

## Dry run

To build everything locally without publishing:

1. Install GoReleaser: `brew install goreleaser`.
2. Run `goreleaser release --snapshot --clean`.
3. Look in `dist/`. It holds the archives, `checksums.txt`, and the generated cask in `dist/homebrew/Casks/`.

## Without the signing secrets

The macOS binaries are unsigned. macOS blocks an unsigned binary that arrives through a browser download or a Homebrew cask, because both mark it as quarantined. The cask does not remove the quarantine: that would bypass Gatekeeper instead of satisfying it. Until signing is set up, macOS users install with `go install github.com/ozzyphantom/SuperDirectory@latest`.

---

This file was written by Claude (AI) from the release configuration. Check each step against the current GitHub and Apple screens before you rely on it.
