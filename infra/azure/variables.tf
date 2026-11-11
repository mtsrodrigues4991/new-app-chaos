variable "subscription_id" {
  type    = string
  default = "488eeea4-6eba-44ae-ba31-a4b8a95b7f69"
}

variable "resource_group" {
  type    = string
  default = "new-app"
}

variable "location" {
  type    = string
  default = "eastus2"
}

variable "dns_label" {
  description = "Começo do endereço público: <dns_label>-<sufixo>.<região>.cloudapp.azure.com"
  type        = string
  default     = "new-app-chaos"
}

variable "cluster_enabled" {
  description = "true = cria o AKS (Ligar); false = apaga só o cluster (Desligar)."
  type        = bool
  default     = false
}

# Cota da assinatura: 10 vCPU normais na região e 3 spot (família DASv4).
variable "apps_vm_size" {
  type    = string
  default = "Standard_D4as_v4"
}

variable "apps_node_count" {
  type    = number
  default = 2
}

variable "obs_vm_size" {
  type    = string
  default = "Standard_D2as_v4"
}

variable "load_vm_size" {
  type    = string
  default = "Standard_D2as_v4"
}

variable "load_spot" {
  description = "Injetora do k6 em máquina spot (mais barata, pode ser retomada pela Azure)."
  type        = bool
  default     = true
}

variable "monthly_budget_usd" {
  type    = number
  default = 30
}
