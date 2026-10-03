import {
  formatBytes,
  getLatestRelease,
  latestAssetUrl,
  REPO_URL,
} from "@/lib/github";

const FALLBACK_ASSETS = [
  "pm0-linux-amd64",
  "pm0-linux-arm64",
  "pm0-linux-arm",
  "pm0-linux-386",
  "pm0-linux-amd64.tar.gz",
  "pm0-linux-arm64.tar.gz",
  "checksums.txt",
];

/**
 * Dynamic "All release assets" table rendered from the latest GitHub release.
 * Falls back to the known static asset list when the API is rate-limited.
 */
export async function ReleaseAssets() {
  const release = await getLatestRelease();
  const version = release?.version;
  const assets = release?.assets?.length
    ? release.assets.map((a) => ({
        name: a.name,
        size: formatBytes(a.size),
        url: a.download_url,
      }))
    : FALLBACK_ASSETS.map((name) => ({
        name,
        size: "—",
        url: latestAssetUrl(name),
      }));

  return (
    <div>
      <p className="mb-3 text-sm text-fd-muted-foreground">
        All assets from the latest release
        {version ? (
          <>
            {" "}
            (<code className="font-mono">v{version}</code>)
          </>
        ) : null}
        . Sizes come from the GitHub API and may be cached for up to an hour.
      </p>
      <div className="overflow-x-auto rounded-xl border not-prose">
        <table className="w-full min-w-[520px] border-collapse text-sm">
          <thead>
            <tr className="border-b bg-fd-card text-left">
              <th scope="col" className="px-4 py-3 font-medium">File</th>
              <th scope="col" className="px-4 py-3 font-medium">Size</th>
              <th scope="col" className="px-4 py-3 font-medium text-right">
                Download
              </th>
            </tr>
          </thead>
          <tbody className="divide-y">
            {assets.map((a) => (
              <tr key={a.name} className="transition-colors hover:bg-fd-muted/50">
                <td className="px-4 py-2.5 font-mono text-[0.8125rem]">{a.name}</td>
                <td className="px-4 py-2.5 font-mono text-[0.8125rem] text-fd-muted-foreground">
                  {a.size}
                </td>
                <td className="px-4 py-2.5 text-right">
                  <a
                    href={a.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-fd-primary hover:underline"
                  >
                    download
                  </a>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-3 text-sm text-fd-muted-foreground">
        SHA-256 digests are published in{" "}
        <a
          href={latestAssetUrl("checksums.txt")}
          target="_blank"
          rel="noopener noreferrer"
          className="text-fd-primary hover:underline"
        >
          checksums.txt
        </a>{" "}
        on every release. Release pages live at{" "}
        <a
          href={`${REPO_URL}/releases`}
          target="_blank"
          rel="noopener noreferrer"
          className="text-fd-primary hover:underline"
        >
          github.com/thakurdotdev/pm0/releases
        </a>
        .
      </p>
    </div>
  );
}
