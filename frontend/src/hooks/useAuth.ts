import { useEffect, useState } from "react";
import { API_PATHS } from "@/lib/apiPaths";
import { APIClientError, apiClient } from "@/lib/apiClient";

interface AuthState {
  isAuthenticated: boolean;
  isLoading: boolean;
  authTarget: ResolvedAuthTarget | "";
  walletAddress: string;
}

interface LoginResult {
  success: boolean;
  error?: string;
}

interface WalletAuthStatusPayload {
  authenticated: boolean;
  wallet_address?: string;
}

interface WalletAuthChallengePayload {
  challenge_id: string;
  siwe_message: string;
}

interface WalletAuthLoginPayload {
  wallet_address?: string;
}

type AuthTarget = "admin" | "agent" | "auto";
type ResolvedAuthTarget = "admin" | "agent";

type EthereumProvider = {
  request<T>(args: { method: string; params?: unknown[] }): Promise<T>;
};

declare global {
  interface Window {
    ethereum?: EthereumProvider;
  }
}

const authPaths = {
  admin: {
    challenge: API_PATHS.admin.authChallenge,
    login: API_PATHS.admin.authLogin,
    logout: API_PATHS.admin.logout,
    status: API_PATHS.admin.authStatus,
  },
  agent: {
    challenge: API_PATHS.agent.authChallenge,
    login: API_PATHS.agent.authLogin,
    logout: API_PATHS.agent.authLogout,
    status: API_PATHS.agent.authStatus,
  },
} as const;

function authCandidates(target: AuthTarget, preferred?: ResolvedAuthTarget | ""): ResolvedAuthTarget[] {
  if (target === "admin" || target === "agent") {
    return [target];
  }
  if (preferred === "admin") {
    return ["admin", "agent"];
  }
  if (preferred === "agent") {
    return ["agent", "admin"];
  }
  return ["admin", "agent"];
}

function emptyAuthState(target: ResolvedAuthTarget | "" = ""): AuthState {
  return {
    isAuthenticated: false,
    isLoading: false,
    authTarget: target,
    walletAddress: "",
  };
}

async function fetchAuthState(target: AuthTarget, preferred?: ResolvedAuthTarget | ""): Promise<AuthState> {
  for (const candidate of authCandidates(target, preferred)) {
    try {
      const data = await apiClient.get<WalletAuthStatusPayload>(authPaths[candidate].status);
      return {
        isAuthenticated: data.authenticated,
        isLoading: false,
        authTarget: candidate,
        walletAddress: data.wallet_address || "",
      };
    } catch {
      continue;
    }
  }
  return emptyAuthState(target === "admin" || target === "agent" ? target : "");
}

function ethereumProvider(): EthereumProvider | undefined {
  return window.ethereum;
}

export function useAuth(target: AuthTarget = "admin") {
  const [authState, setAuthState] = useState<AuthState>({
    isAuthenticated: false,
    isLoading: true,
    authTarget: "",
    walletAddress: "",
  });

  const checkAuth = async () => {
    setAuthState(await fetchAuthState(target, authState.authTarget));
  };

  useEffect(() => {
    void (async () => {
      setAuthState(await fetchAuthState(target));
    })();
  }, [target]);

  const login = async (): Promise<LoginResult> => {
    try {
      const provider = ethereumProvider();
      if (!provider) {
        return { success: false, error: "Wallet provider is unavailable." };
      }
      const accounts = await provider.request<string[]>({
        method: "eth_requestAccounts",
      });
      const address = accounts?.[0]?.trim();
      if (!address) {
        return { success: false, error: "Wallet account is unavailable." };
      }
      let authTarget = authState.authTarget;
      if (!authTarget) {
        const nextState = await fetchAuthState(target);
        setAuthState(nextState);
        authTarget = nextState.authTarget;
      }
      if (!authTarget) {
        return { success: false, error: "Wallet login is unavailable." };
      }

      const challenge = await apiClient.post<WalletAuthChallengePayload>(
        authPaths[authTarget].challenge,
        { address }
      );
      const signature = await provider.request<string>({
        method: "personal_sign",
        params: [challenge.siwe_message, address],
      });
      const data = await apiClient.post<WalletAuthLoginPayload>(
        authPaths[authTarget].login,
        {
          challenge_id: challenge.challenge_id,
          siwe_message: challenge.siwe_message,
          siwe_signature: signature,
        }
      );
      setAuthState((prev) => ({
        ...prev,
        isAuthenticated: true,
        authTarget,
        walletAddress: data.wallet_address || address,
      }));
      return { success: true };
    } catch (err: unknown) {
      if (err instanceof APIClientError) {
        return {
          success: false,
          error: err.message || "Wallet login failed.",
        };
      }

      return {
        success: false,
        error: err instanceof Error ? err.message : "Wallet login failed.",
      };
    }
  };

  const logout = async () => {
    const candidates = authCandidates(target, authState.authTarget);
    for (const candidate of candidates) {
      try {
        await apiClient.post<unknown>(authPaths[candidate].logout);
        break;
      } catch {
        continue;
      }
    }
    setAuthState((prev) => ({ ...prev, isAuthenticated: false, walletAddress: "" }));
  };

  return {
    isAuthenticated: authState.isAuthenticated,
    isLoading: authState.isLoading,
    walletAddress: authState.walletAddress,
    login,
    logout,
    checkAuth,
  };
}
