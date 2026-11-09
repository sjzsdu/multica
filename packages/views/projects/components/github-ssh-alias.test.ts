import { describe, expect, it } from "vitest";

import { applyGitHubSSHHostAlias } from "./github-ssh-alias";

describe("applyGitHubSSHHostAlias", () => {
  it.each([
    ["git@github.com:company/repo.git", "git@github-work:company/repo.git"],
    ["ssh://git@github.com/company/repo.git", "ssh://git@github-work/company/repo.git"],
    ["https://github.com/company/repo.git", "git@github-work:company/repo.git"],
  ])("rewrites %s", (input, expected) => {
    expect(applyGitHubSSHHostAlias(input, "github-work")).toEqual({
      url: expected,
      applied: true,
    });
  });

  it("does not claim an alias for non-GitHub URLs or invalid aliases", () => {
    expect(applyGitHubSSHHostAlias("git@gitlab.example.com:group/repo.git", "github-work")).toEqual({
      url: "git@gitlab.example.com:group/repo.git",
      applied: false,
    });
    expect(applyGitHubSSHHostAlias("https://github.com/company/repo.git", "bad alias")).toEqual({
      url: "https://github.com/company/repo.git",
      applied: false,
    });
  });
});
