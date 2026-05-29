import { useEffect, useState } from "react";
import {
  useAccount,
  useConnect,
  useConnectors,
  useDisconnect,
  useSignMessage,
} from "wagmi";
import { API_PATHS } from "@/lib/apiPaths";
import { APIClientError, apiClient } from "@/lib/apiClient";
import { writeAdminAuthToken } from "@/lib/adminAuthToken";
import type {
  WalletAuthChallengeResponse,
  WalletAuthLoginResponse,
  WalletAuthStatusResponse,
} from "@/types/api";

interface AuthState {
  isAuthenticated: boolean;
  isLoading: boolean;
  walletAddress: string;
}

interface LoginResult {
  success: boolean;
  error?: string;
}

function emptyAuthState(): AuthState {
  return {
    isAuthenticated: false,
    isLoading: false,
    walletAddress: "",
  };
}

async function fetchAuthState(): Promise<AuthState> {
  try {
    const data = await apiClient.get<WalletAuthStatusResponse>(
      API_PATHS.admin.authStatus
    );
    return {
      isAuthenticated: data.authenticated,
      isLoading: false,
      walletAddress: data.wallet_address || "",
    };
  } catch {
    return emptyAuthState();
  }
}

export function useAuth() {
  const { address: connectedAddress, isConnected } = useAccount();
  const connectors = useConnectors();
  const { connectAsync } = useConnect();
  const { disconnectAsync } = useDisconnect();
  const { signMessageAsync } = useSignMessage();
  const [authState, setAuthState] = useState<AuthState>({
    isAuthenticated: false,
    isLoading: true,
    walletAddress: "",
  });

  const checkAuth = async () => {
    setAuthState(await fetchAuthState());
  };

  useEffect(() => {
    void (async () => {
      setAuthState(await fetchAuthState());
    })();
  }, []);

  const login = async (): Promise<LoginResult> => {
    try {
      let address = connectedAddress;
      if (!isConnected || !address) {
        const connector = connectors[0];
        if (!connector) {
          return { success: false, error: "Wallet connector is unavailable." };
        }
        const connected = await connectAsync({ connector });
        address = connected.accounts[0];
      }
      if (!address) {
        return { success: false, error: "Wallet provider is unavailable." };
      }
      const challenge = await apiClient.post<WalletAuthChallengeResponse>(
        API_PATHS.admin.authChallenge,
        { address }
      );
      const signature = await signMessageAsync({
        account: address,
        message: challenge.siwe_message,
      });
      const data = await apiClient.post<WalletAuthLoginResponse>(
        API_PATHS.admin.authLogin,
        {
          challenge_id: challenge.challenge_id,
          siwe_message: challenge.siwe_message,
          siwe_signature: signature,
        }
      );
      const accessToken = data.access_token?.trim() || "";
      if (!accessToken) {
        writeAdminAuthToken("");
        return { success: false, error: "Admin login did not return an access token." };
      }
      writeAdminAuthToken(accessToken);
      setAuthState((prev) => ({
        ...prev,
        isAuthenticated: true,
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
    try {
      await apiClient.post<unknown>(API_PATHS.admin.logout);
    } catch {
      // Logging out should clear local state even if the remote token is stale.
    } finally {
      writeAdminAuthToken("");
    }
    setAuthState((prev) => ({ ...prev, isAuthenticated: false, walletAddress: "" }));
    try {
      await disconnectAsync();
    } catch {
      // Some wallet connectors cannot be disconnected programmatically.
    }
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
